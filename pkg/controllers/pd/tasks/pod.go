// Copyright 2024 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tasks

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/pingcap/tidb-operator/apis/core/v1alpha1"
	"github.com/pingcap/tidb-operator/pkg/client"
	"github.com/pingcap/tidb-operator/pkg/image"
	"github.com/pingcap/tidb-operator/pkg/overlay"
	pdm "github.com/pingcap/tidb-operator/pkg/timanager/pd"
	"github.com/pingcap/tidb-operator/pkg/utils/k8s"
	maputil "github.com/pingcap/tidb-operator/pkg/utils/map"
	"github.com/pingcap/tidb-operator/pkg/utils/task/v3"
	"github.com/pingcap/tidb-operator/third_party/kubernetes/pkg/controller/statefulset"
)

const (
	defaultReadinessProbeInitialDelaySeconds = 5
)

func TaskPod(ctx *ReconcileContext, logger logr.Logger, c client.Client) task.Task {
	return task.NameTaskFunc("ConfigMap", func() task.Result {
		expected := newPod(ctx.Cluster, ctx.PDGroup, ctx.PD, ctx.ConfigHash)
		if ctx.Pod == nil {
			// We have to refresh cache of members to make sure a pd without pod is unhealthy.
			// If the healthy info is out of date, the operator may mark this pd up-to-date unexpectedly
			// and begin to update the next PD.
			if ctx.Healthy {
				ctx.PDClient.Members().Refresh()
				return task.Wait().With("wait until pd's status becomes unhealthy")
			}
			if err := c.Apply(ctx, expected); err != nil {
				return task.Fail().With("can't create pod of pd: %v", err)
			}
			ctx.SetPod(expected)
			return task.Complete().With("pod is synced")
		}

		res := k8s.ComparePods(ctx.Pod, expected)
		curHash, expectHash := ctx.Pod.Labels[v1alpha1.LabelKeyConfigHash], expected.Labels[v1alpha1.LabelKeyConfigHash]
		configChanged := curHash != expectHash
		logger.Info("compare pod", "result", res, "configChanged", configChanged, "currentConfigHash", curHash, "expectConfigHash", expectHash)

		if res == k8s.CompareResultRecreate ||
			(configChanged && ctx.PDGroup.Spec.ConfigUpdateStrategy == v1alpha1.ConfigUpdateStrategyRollingUpdate) {
			// NOTE: both rtx.Healthy and rtx.Pod are not always newest
			// So pre delete check may also be skipped in some cases, for example,
			// the PD is just started.
			if ctx.Healthy || statefulset.IsPodReady(ctx.Pod) {
				wait, err := preDeleteCheck(ctx, logger, ctx.PDClient, ctx.PD, ctx.Peers, ctx.IsLeader)
				if err != nil {
					return task.Fail().With("can't delete pod of pd: %v", err)
				}

				if wait {
					return task.Wait().With("wait for pd leader being transferred")
				}
			}

			logger.Info("will delete the pod to recreate", "name", ctx.Pod.Name, "namespace", ctx.Pod.Namespace, "UID", ctx.Pod.UID)

			if err := c.Delete(ctx, ctx.Pod); err != nil {
				return task.Fail().With("can't delete pod of pd: %v", err)
			}

			ctx.PodIsTerminating = true

			return task.Complete().With("pod is deleting")
		} else if res == k8s.CompareResultUpdate {
			logger.Info("will update the pod in place")
			if err := c.Apply(ctx, expected); err != nil {
				return task.Fail().With("can't apply pod of pd: %v", err)
			}
			ctx.SetPod(expected)
		}

		return task.Complete().With("pod is synced")
	})
}

func preDeleteCheck(
	ctx context.Context,
	logger logr.Logger,
	pdc pdm.PDClient,
	pd *v1alpha1.PD,
	peers []*v1alpha1.PD,
	isLeader bool,
) (bool, error) {
	// TODO: add quorum check. After stopping this pd, quorum should not be lost

	if isLeader {
		peer := LongestHealthPeer(pd, peers)
		if peer == "" {
			return false, fmt.Errorf("no healthy transferee available")
		}

		logger.Info("try to transfer leader", "from", pd.Name, "to", peer)

		if err := pdc.Underlay().TransferPDLeader(ctx, peer); err != nil {
			return false, fmt.Errorf("transfer leader failed: %w", err)
		}

		return true, nil
	}

	return false, nil
}

func newPod(cluster *v1alpha1.Cluster, pdg *v1alpha1.PDGroup, pd *v1alpha1.PD, configHash string) *corev1.Pod {
	vols := []corev1.Volume{
		{
			Name: v1alpha1.VolumeNameConfig,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ConfigMapName(pd.Name),
					},
				},
			},
		},
	}

	mounts := []corev1.VolumeMount{
		{
			Name:      v1alpha1.VolumeNameConfig,
			MountPath: v1alpha1.DirNameConfigPD,
		},
	}

	for i := range pd.Spec.Volumes {
		vol := &pd.Spec.Volumes[i]
		name := v1alpha1.NamePrefix + "pd"
		if vol.Name != "" {
			name = name + "-" + vol.Name
		}
		vols = append(vols, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: PersistentVolumeClaimName(pd.Name, vol.Name),
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: vol.Path,
		})
	}

	if cluster.IsTLSClusterEnabled() {
		groupName := pd.Labels[v1alpha1.LabelKeyGroup]
		vols = append(vols, corev1.Volume{
			Name: v1alpha1.PDClusterTLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: cluster.TLSClusterSecretName(groupName),
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      v1alpha1.PDClusterTLSVolumeName,
			MountPath: v1alpha1.PDClusterTLSMountPath,
			ReadOnly:  true,
		})

		if pdg.MountClusterClientSecret() {
			vols = append(vols, corev1.Volume{
				Name: v1alpha1.ClusterTLSClientVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: cluster.ClusterClientTLSSecretName(),
					},
				},
			})
			mounts = append(mounts, corev1.VolumeMount{
				Name:      v1alpha1.ClusterTLSClientVolumeName,
				MountPath: v1alpha1.ClusterTLSClientMountPath,
				ReadOnly:  true,
			})
		}
	}

	anno := maputil.Copy(pd.GetAnnotations())
	// TODO: should not inherit all labels and annotations into pod
	delete(anno, v1alpha1.AnnoKeyInitialClusterNum)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: pd.Namespace,
			Name:      pd.Name,
			Labels: maputil.Merge(pd.Labels, map[string]string{
				v1alpha1.LabelKeyInstance:   pd.Name,
				v1alpha1.LabelKeyConfigHash: configHash,
			}),
			Annotations: anno,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(pd, v1alpha1.SchemeGroupVersion.WithKind("PD")),
			},
		},
		Spec: corev1.PodSpec{
			Hostname:     pd.Name,
			Subdomain:    pd.Spec.Subdomain,
			NodeSelector: pd.Spec.Topology,
			Containers: []corev1.Container{
				{
					Name:            v1alpha1.ContainerNamePD,
					Image:           image.PD.Image(pd.Spec.Image, pd.Spec.Version),
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command: []string{
						"/pd-server",
						"--config",
						filepath.Join(v1alpha1.DirNameConfigPD, v1alpha1.ConfigFileName),
					},
					Ports: []corev1.ContainerPort{
						{
							Name:          v1alpha1.PDPortNameClient,
							ContainerPort: pd.GetClientPort(),
						},
						{
							Name:          v1alpha1.PDPortNamePeer,
							ContainerPort: pd.GetPeerPort(),
						},
					},
					VolumeMounts:   mounts,
					Resources:      k8s.GetResourceRequirements(pd.Spec.Resources),
					ReadinessProbe: buildPDReadinessProbe(pd.GetClientPort()),
				},
			},
			Volumes: vols,
		},
	}

	if pd.Spec.Overlay != nil {
		overlay.OverlayPod(pod, pd.Spec.Overlay.Pod)
	}

	k8s.CalculateHashAndSetLabels(pod)
	return pod
}

func buildPDReadinessProbe(port int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.FromInt32(port),
			},
		},
		InitialDelaySeconds: defaultReadinessProbeInitialDelaySeconds,
	}
}
