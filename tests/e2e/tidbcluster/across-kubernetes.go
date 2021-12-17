// Copyright 2021 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package tidbcluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	asclientset "github.com/pingcap/advanced-statefulset/client/client/clientset/versioned"
	"github.com/pingcap/tidb-operator/pkg/apis/pingcap/v1alpha1"
	"github.com/pingcap/tidb-operator/pkg/client/clientset/versioned"
	"github.com/pingcap/tidb-operator/pkg/controller"
	"github.com/pingcap/tidb-operator/pkg/scheme"
	"github.com/pingcap/tidb-operator/tests"
	e2econfig "github.com/pingcap/tidb-operator/tests/e2e/config"
	e2eframework "github.com/pingcap/tidb-operator/tests/e2e/framework"
	utilimage "github.com/pingcap/tidb-operator/tests/e2e/util/image"
	nsutil "github.com/pingcap/tidb-operator/tests/e2e/util/ns"
	"github.com/pingcap/tidb-operator/tests/e2e/util/portforward"
	utiltidb "github.com/pingcap/tidb-operator/tests/e2e/util/tidb"
	utiltc "github.com/pingcap/tidb-operator/tests/e2e/util/tidbcluster"
	"github.com/pingcap/tidb-operator/tests/pkg/fixture"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	aggregatorclient "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset"
	"k8s.io/kubernetes/test/e2e/framework"
	ctrlCli "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultClusterDomain = "cluster.local"
)

var _ = ginkgo.Describe("[Across Kubernetes]", func() {
	f := e2eframework.NewDefaultFramework("across-kubernetes")

	var (
		namespaces []string
		c          clientset.Interface
		cli        versioned.Interface
		asCli      asclientset.Interface
		aggrCli    aggregatorclient.Interface
		apiExtCli  apiextensionsclientset.Interface
		oa         *tests.OperatorActions
		cfg        *tests.Config
		config     *restclient.Config
		ocfg       *tests.OperatorConfig
		genericCli ctrlCli.Client
		fwCancel   context.CancelFunc
		fw         portforward.PortForward
	)

	ginkgo.BeforeEach(func() {
		var err error

		// build var
		namespaces = []string{f.Namespace.Name}
		c = f.ClientSet
		config, err = framework.LoadConfig()
		framework.ExpectNoError(err, "failed to load config")
		cli, err = versioned.NewForConfig(config)
		framework.ExpectNoError(err, "failed to create clientset for pingcap")
		asCli, err = asclientset.NewForConfig(config)
		framework.ExpectNoError(err, "failed to create clientset for advanced-statefulset")
		genericCli, err = ctrlCli.New(config, ctrlCli.Options{Scheme: scheme.Scheme})
		framework.ExpectNoError(err, "failed to create clientset for controller-runtime")
		aggrCli, err = aggregatorclient.NewForConfig(config)
		framework.ExpectNoError(err, "failed to create clientset kube-aggregator")
		apiExtCli, err = apiextensionsclientset.NewForConfig(config)
		framework.ExpectNoError(err, "failed to create clientset apiextensions-apiserver")
		clientRawConfig, err := e2econfig.LoadClientRawConfig()
		framework.ExpectNoError(err, "failed to load raw config for tidb-operator")
		ctx, cancel := context.WithCancel(context.Background())
		fw, err = portforward.NewPortForwarder(ctx, e2econfig.NewSimpleRESTClientGetter(clientRawConfig))
		framework.ExpectNoError(err, "failed to create port forwarder")
		fwCancel = cancel
		cfg = e2econfig.TestConfig
		ocfg = e2econfig.NewDefaultOperatorConfig(cfg)
		oa = tests.NewOperatorActions(cli, c, asCli, aggrCli, apiExtCli, tests.DefaultPollInterval, ocfg, e2econfig.TestConfig, nil, fw, f)
	})

	ginkgo.JustBeforeEach(func() {
		// create all namespaces except framework's namespace
		for _, ns := range namespaces[1:] {
			ginkgo.By(fmt.Sprintf("Building namespace %s", ns))
			_, existed, err := nsutil.CreateNamespaceIfNeeded(ns, c, nil)
			framework.ExpectEqual(existed, false, fmt.Sprintf("namespace %s is existed", ns))
			framework.ExpectNoError(err, fmt.Sprintf("failed to create namespace %s", ns))
		}
	})

	ginkgo.JustAfterEach(func() {
		// delete all namespaces if needed except framework's namespace
		if framework.TestContext.DeleteNamespace && (framework.TestContext.DeleteNamespaceOnFailure || !ginkgo.CurrentGinkgoTestDescription().Failed) {
			for _, ns := range namespaces[1:] {
				ginkgo.By(fmt.Sprintf("Destroying namespace %s", ns))
				_, err := nsutil.DeleteNamespace(ns, c)
				framework.ExpectNoError(err, fmt.Sprintf("failed to create namespace %s", ns))
			}
		}
	})

	ginkgo.AfterEach(func() {
		if fwCancel != nil {
			fwCancel()
		}
	})

	ginkgo.Describe("[Deploy]", func() {
		ginkgo.BeforeEach(func() {
			ns1 := namespaces[0]
			namespaces = append(namespaces, ns1+"-1", ns1+"-2")
		})

		version := utilimage.TiDBLatest
		cluster1Domain := defaultClusterDomain
		cluster2Domain := defaultClusterDomain
		cluster3Domain := defaultClusterDomain

		ginkgo.It("Deploy and delete cluster across kubernetes", func() {
			ns1 := namespaces[0]
			ns2 := namespaces[1]
			ns3 := namespaces[2]

			tc1 := GetTCForAcrossKubernetes(ns1, "basic-1", version, cluster1Domain, nil)
			tc2 := GetTCForAcrossKubernetes(ns2, "basic-2", version, cluster2Domain, tc1)
			tc3 := GetTCForAcrossKubernetes(ns3, "basic-3", version, cluster3Domain, tc1)

			ginkgo.By("Deploy the basic cluster-1")
			utiltc.MustCreateTCWithComponentsReady(genericCli, oa, tc1, 10*time.Minute, 10*time.Second)

			ginkgo.By("Deploy the basic cluster-2")
			utiltc.MustCreateTCWithComponentsReady(genericCli, oa, tc2, 10*time.Minute, 10*time.Second)

			ginkgo.By("Deploy the basic cluster-3")
			utiltc.MustCreateTCWithComponentsReady(genericCli, oa, tc3, 10*time.Minute, 10*time.Second)

			ginkgo.By("Check status of all clusters")
			err := CheckClusterDomainEffect(cli, []*v1alpha1.TidbCluster{tc1, tc2, tc3})
			framework.ExpectNoError(err, "failed to check status")

			// connectable test
			_, err = utiltidb.TiDBIsConnectable(fw, tc1.Namespace, tc1.Name, "root", "")()
			framework.ExpectNoError(err, "tc1 are not connectable")
			_, err = utiltidb.TiDBIsConnectable(fw, tc2.Namespace, tc2.Name, "root", "")()
			framework.ExpectNoError(err, "tc2 are not connectable")
			_, err = utiltidb.TiDBIsConnectable(fw, tc3.Namespace, tc3.Name, "root", "")()
			framework.ExpectNoError(err, "tc3 are not connectable")

			ginkgo.By("Scale in cluster-3, and delete the cluster-3")

			framework.ExpectNoError(controller.GuaranteedUpdate(genericCli, tc3, func() error {
				tc3.Spec.PD.Replicas = 0
				tc3.Spec.TiDB.Replicas = 0
				tc3.Spec.TiKV.Replicas = 0
				tc3.Spec.TiFlash.Replicas = 0
				tc3.Spec.TiCDC.Replicas = 0
				tc3.Spec.Pump.Replicas = 0
				return nil
			}), "failed to scale in cluster 3")
			framework.ExpectNoError(genericCli.Delete(context.TODO(), tc3), "failed to delete cluster 3")
			err = wait.PollImmediate(10*time.Second, 3*time.Minute, func() (bool, error) {
				if err := CheckPeerMembersAndClusterStatus(genericCli, tc1, tc3); err != nil {
					return false, nil
				}
				return true, nil
			})
			framework.ExpectNoError(err, "tc1 are not all healthy")
			err = wait.PollImmediate(10*time.Second, 3*time.Minute, func() (bool, error) {
				if err := CheckPeerMembersAndClusterStatus(genericCli, tc2, tc3); err != nil {
					return false, nil
				}
				return true, nil
			})
			framework.ExpectNoError(err, "tc2 are not all healthy")
		})
})

func CheckPeerMembersAndClusterStatus(clusterCli ctrlCli.Client, tc *v1alpha1.TidbCluster, expectNonExistedTc *v1alpha1.TidbCluster) error {
	clusterCli.Get(context.TODO(), types.NamespacedName{Namespace: tc.Namespace, Name: tc.Name}, tc)

	if tc.Status.PD.Phase != v1alpha1.NormalPhase || tc.Status.TiKV.Phase != v1alpha1.NormalPhase || tc.Status.TiDB.Phase != v1alpha1.NormalPhase || tc.Status.TiFlash.Phase != v1alpha1.NormalPhase || tc.Status.Pump.Phase != v1alpha1.NormalPhase || tc.Status.TiCDC.Phase != v1alpha1.NormalPhase {
		return fmt.Errorf("the tc %s is not healthy", tc.Name)
	}

	for _, peerMember := range tc.Status.PD.PeerMembers {
		if strings.Contains(peerMember.Name, expectNonExistedTc.Name) {
			return fmt.Errorf("the PD peer members contain the member belonging to the deleted tc")
		}
	}

	for _, peerStore := range tc.Status.TiKV.PeerStores {
		if strings.Contains(peerStore.PodName, expectNonExistedTc.Name) {
			return fmt.Errorf("the TiKV peer stores contain the store belonging to the deleted tc")
		}
	}

	for _, peerStore := range tc.Status.TiFlash.PeerStores {
		if strings.Contains(peerStore.PodName, expectNonExistedTc.Name) {
			return fmt.Errorf("the TiFlash peer stores contain the store belonging to the deleted tc")
		}
	}
	return nil
}

func GetTCForAcrossKubernetes(ns, name, version, clusterDomain string, joinTC *v1alpha1.TidbCluster) *v1alpha1.TidbCluster {
	tc := fixture.GetTidbCluster(ns, name, version)
	tc = fixture.AddTiFlashForTidbCluster(tc)
	tc = fixture.AddTiCDCForTidbCluster(tc)
	tc = fixture.AddPumpForTidbCluster(tc)

	tc.Spec.PD.Replicas = 1
	tc.Spec.TiDB.Replicas = 1
	tc.Spec.TiKV.Replicas = 1
	tc.Spec.TiFlash.Replicas = 1
	tc.Spec.TiCDC.Replicas = 1
	tc.Spec.Pump.Replicas = 1

	tc.Spec.ClusterDomain = clusterDomain
	if joinTC != nil {
		tc.Spec.Cluster = &v1alpha1.TidbClusterRef{
			Namespace:     joinTC.Namespace,
			Name:          joinTC.Name,
			ClusterDomain: joinTC.Spec.ClusterDomain,
		}
	}

	return tc
}

func CheckClusterDomainEffect(cli versioned.Interface, tidbclusters []*v1alpha1.TidbCluster) error {
	// we use deploy cluster in multi namespace to mock deploying across kubernetes,
	// so we need to check status of tc to ensure `clusterDomain` is effect.

	for i := range tidbclusters {
		tc, err := cli.PingcapV1alpha1().TidbClusters(tidbclusters[i].Namespace).
			Get(context.TODO(), tidbclusters[i].Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		tidbclusters[i] = tc
	}

	for _, tc := range tidbclusters {
		pdNameSuffix := fmt.Sprintf("%s-pd-peer.%s.svc.%s", tc.Name, tc.Namespace, tc.Spec.ClusterDomain)
		tikvIPSuffix := fmt.Sprintf("%s-tikv-peer.%s.svc.%s", tc.Name, tc.Namespace, tc.Spec.ClusterDomain)
		tiflashIPSuffix := fmt.Sprintf("%s-tiflash-peer.%s.svc.%s", tc.Name, tc.Namespace, tc.Spec.ClusterDomain)
		pumpHostSuffix := fmt.Sprintf("%s-pump.%s.svc.%s", tc.Name, tc.Namespace, tc.Spec.ClusterDomain)

		// pd
		if tc.Spec.PD != nil && tc.Spec.PD.Replicas != 0 {
			for name, info := range tc.Status.PD.Members {
				// check member format
				ok := strings.HasSuffix(name, pdNameSuffix)
				if !ok {
					return fmt.Errorf("pd name %s don't have suffix %s", name, pdNameSuffix)
				}
				ok = strings.HasSuffix(info.Name, pdNameSuffix)
				if !ok {
					return fmt.Errorf("pd name %s don't have suffix %s", name, pdNameSuffix)
				}
				ok = strings.Contains(info.ClientURL, pdNameSuffix)
				if !ok {
					return fmt.Errorf("pd clientURL %s don't contain %s", name, pdNameSuffix)
				}

				// check if member exist in other tc
				for _, othertc := range tidbclusters {
					if othertc == tc {
						continue
					}
					exist := false
					for peerName := range othertc.Status.PD.PeerMembers {
						if peerName == name {
							exist = true
							break
						}
					}
					if !exist {
						return fmt.Errorf("pd name %s don't exist in other tc %s/%s", name, othertc.Namespace, othertc.Name)
					}
				}
			}
		}
		// tikv
		if tc.Spec.TiKV != nil && tc.Spec.TiKV.Replicas != 0 {
			for _, ownStore := range tc.Status.TiKV.Stores {
				// check store format
				ok := strings.HasSuffix(ownStore.IP, tikvIPSuffix)
				if !ok {
					return fmt.Errorf("tikv store ip %s don't have suffix %s", ownStore.IP, tiflashIPSuffix)
				}

				// check if store exist in other tc
				for _, othertc := range tidbclusters {
					if othertc == tc {
						continue
					}
					exist := false
					for _, peerStore := range othertc.Status.TiKV.PeerStores {
						if peerStore.IP == ownStore.IP {
							exist = true
							break
						}
					}
					if !exist {
						return fmt.Errorf("tikv store ip %s don't exist in other tc %s/%s",
							ownStore.IP, othertc.Namespace, othertc.Name)
					}
				}
			}

		}
		// tiflash
		if tc.Spec.TiFlash != nil && tc.Spec.TiFlash.Replicas != 0 {
			for _, ownStore := range tc.Status.TiFlash.Stores {
				// check store format
				ok := strings.HasSuffix(ownStore.IP, tiflashIPSuffix)
				if !ok {
					return fmt.Errorf("tiflash store ip %s don't have suffix %s", ownStore.IP, tiflashIPSuffix)
				}

				// check if store exist in other tc
				for _, othertc := range tidbclusters {
					if othertc == tc {
						continue
					}
					exist := false
					for _, peerStore := range othertc.Status.TiFlash.PeerStores {
						if peerStore.IP == ownStore.IP {
							exist = true
							break
						}
					}
					if !exist {
						return fmt.Errorf("tiflash store ip %s don't exist in other tc %s/%s",
							ownStore.IP, othertc.Namespace, othertc.Name)
					}
				}
			}

		}
		// pump
		if tc.Spec.Pump != nil && tc.Spec.Pump.Replicas != 0 {
			// check member format
			ownMembers := []*v1alpha1.PumpNodeStatus{}
			for _, member := range tc.Status.Pump.Members {
				if strings.Contains(member.Host, pumpHostSuffix) {
					ownMembers = append(ownMembers, member)
					break
				}
			}
			if len(ownMembers) == 0 {
				return fmt.Errorf("all pump member hosts don't contain %s", pumpHostSuffix)
			}

			// check if member exist in other tc
			for _, ownMember := range ownMembers {
				for _, othertc := range tidbclusters {
					if othertc == tc {
						continue
					}
					exist := false
					for _, peerMember := range othertc.Status.Pump.Members {
						if peerMember.Host == ownMember.Host {
							exist = true
							break
						}
					}
					if !exist {
						return fmt.Errorf("pump member host %s don't exist in other tc %s/%s",
							ownMember.Host, othertc.Namespace, othertc.Name)
					}
				}
			}

		}
	}

	return nil
}
