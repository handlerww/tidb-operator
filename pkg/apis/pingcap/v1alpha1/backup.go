// Copyright 2019 PingCAP, Inc.
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

package v1alpha1

import (
	"fmt"

	"github.com/pingcap/tidb-operator/pkg/apis/label"
	"github.com/pingcap/tidb-operator/pkg/apis/util/config"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	DefaultBatchDeleteOption = BatchDeleteOption{
		BatchConcurrency:   10,
		RoutineConcurrency: 100,
	}

	// defaultCleanOption is default clean option
	defaultCleanOption = CleanOption{
		PageSize:          10000,
		RetryCount:        5,
		BatchDeleteOption: DefaultBatchDeleteOption,
	}
)

// GetCleanJobName return the clean job name
func (bk *Backup) GetCleanJobName() string {
	return fmt.Sprintf("clean-%s", bk.GetName())
}

// GetCleanJobName return the clean job name for log backup
func (bk *Backup) GetStopLogBackupJobName() string {
	return fmt.Sprintf("stop-%s", bk.GetName())
}

// GetBackupJobName return the backup job name
func (bk *Backup) GetBackupJobName() string {
	if command := ParseLogBackupSubcommand(bk); command != "" {
		return fmt.Sprintf("backup-%s-%s", bk.GetName(), command)
	}
	return fmt.Sprintf("backup-%s", bk.GetName())
}

func (bk *Backup) GetVolumeBackupInitializeJobName() string {
	backupJobName := bk.GetBackupJobName()
	return fmt.Sprintf("%s-init", backupJobName)
}

// GetAllLogBackupJobName return the all log backup job name
func (bk *Backup) GetAllLogBackupJobName() []string {
	return []string{
		fmt.Sprintf("backup-%s-%s", bk.GetName(), LogStartCommand),
		fmt.Sprintf("backup-%s-%s", bk.GetName(), LogStopCommand),
		fmt.Sprintf("backup-%s-%s", bk.GetName(), LogTruncateCommand),
	}
}

// GetTidbEndpointHash return the hash string base on tidb cluster's host and port
func (bk *Backup) GetTidbEndpointHash() string {
	return HashContents([]byte(bk.Spec.From.GetTidbEndpoint()))
}

// GetBackupPVCName return the backup pvc name
func (bk *Backup) GetBackupPVCName() string {
	return fmt.Sprintf("backup-pvc-%s", bk.GetTidbEndpointHash())
}

// GetInstanceName return the backup instance name
func (bk *Backup) GetInstanceName() string {
	if bk.Labels != nil {
		if v, ok := bk.Labels[label.InstanceLabelKey]; ok {
			return v
		}
	}
	return bk.Name
}

// GetCleanOption return the clean option
func (bk *Backup) GetCleanOption() CleanOption {
	if bk.Spec.CleanOption == nil {
		return defaultCleanOption
	}

	ropt := *bk.Spec.CleanOption
	if ropt.PageSize <= 0 {
		ropt.PageSize = defaultCleanOption.PageSize
	}
	if ropt.RetryCount <= 0 {
		ropt.RetryCount = defaultCleanOption.RetryCount
	}
	if ropt.BatchConcurrency <= 0 {
		ropt.BatchConcurrency = defaultCleanOption.BatchConcurrency
	}
	if ropt.RoutineConcurrency <= 0 {
		ropt.RoutineConcurrency = defaultCleanOption.RoutineConcurrency
	}

	return ropt
}

// GetBackupCondition get the specify type's BackupCondition from the given BackupStatus
func GetBackupCondition(status *BackupStatus, conditionType BackupConditionType) (int, *BackupCondition) {
	if status == nil {
		return -1, nil
	}
	for i := range status.Conditions {
		if status.Conditions[i].Type == conditionType {
			return i, &status.Conditions[i]
		}
	}
	return -1, nil
}

// UpdateBackupCondition updates existing Backup condition or creates a new
// one. Sets LastTransitionTime to now if the status has changed.
// Returns true if Backup condition has changed or has been added.
func UpdateBackupCondition(status *BackupStatus, condition *BackupCondition) bool {
	if condition == nil {
		return false
	}
	condition.LastTransitionTime = metav1.Now()
	// Try to find this Backup condition.
	conditionIndex, oldCondition := GetBackupCondition(status, condition.Type)

	isDiffPhase := status.Phase != condition.Type

	// restart condition no need to update to phase
	if isDiffPhase && condition.Type != BackupRestart {
		status.Phase = condition.Type
	}

	if oldCondition == nil {
		// We are adding new Backup condition.
		status.Conditions = append(status.Conditions, *condition)
		return true
	}

	// if phase is diff, we need update condition
	if isDiffPhase {
		status.Conditions[conditionIndex] = *condition
		return true
	}

	// We are updating an existing condition, so we need to check if it has changed.
	if condition.Status == oldCondition.Status {
		condition.LastTransitionTime = oldCondition.LastTransitionTime
	}

	isUpdate := condition.Status == oldCondition.Status &&
		condition.Reason == oldCondition.Reason &&
		condition.Message == oldCondition.Message &&
		condition.LastTransitionTime.Equal(&oldCondition.LastTransitionTime)

	status.Conditions[conditionIndex] = *condition
	// Return true if one of the fields have changed.
	return !isUpdate
}

// IsBackupComplete returns true if a Backup has successfully completed
func IsBackupComplete(backup *Backup) bool {
	if backup.Spec.Mode == BackupModeLog {
		return IsLogBackupSubCommandOntheCondition(backup, BackupComplete)
	}
	_, condition := GetBackupCondition(&backup.Status, BackupComplete)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsBackupInvalid returns true if a Backup has invalid condition set
func IsBackupInvalid(backup *Backup) bool {
	if backup.Spec.Mode == BackupModeLog {
		return IsLogBackupSubCommandOntheCondition(backup, BackupInvalid)
	}
	_, condition := GetBackupCondition(&backup.Status, BackupInvalid)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsBackupFailed returns true if a Backup has failed
func IsBackupFailed(backup *Backup) bool {
	if backup.Spec.Mode == BackupModeLog {
		return IsLogBackupSubCommandOntheCondition(backup, BackupFailed)
	}
	_, condition := GetBackupCondition(&backup.Status, BackupFailed)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsBackupScheduled returns true if a Backup has successfully scheduled
func IsBackupScheduled(backup *Backup) bool {
	if backup.Spec.Mode == BackupModeLog {
		return IsLogBackupSubCommandOntheCondition(backup, BackupScheduled)
	}
	_, condition := GetBackupCondition(&backup.Status, BackupScheduled)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// HaveTruncateUntil returns true if a Backup has truncate until set
func HaveTruncateUntil(backup *Backup) bool {
	if backup.Spec.Mode != BackupModeLog {
		return false
	}
	return backup.Spec.LogTruncateUntil != ""
}

// IsBackupRunning returns true if a Backup is Running.
func IsBackupRunning(backup *Backup) bool {
	if backup.Spec.Mode == BackupModeLog {
		return IsLogBackupSubCommandOntheCondition(backup, BackupRunning)
	}
	_, condition := GetBackupCondition(&backup.Status, BackupRunning)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsBackupRestart returns true if a Backup was restarted.
func IsBackupRestart(backup *Backup) bool {
	_, hasRestartCondition := GetBackupCondition(&backup.Status, BackupRestart)
	return hasRestartCondition != nil
}

// IsBackupPrepared returns true if a Backup is Prepare.
func IsBackupPrepared(backup *Backup) bool {
	if backup.Spec.Mode == BackupModeLog {
		return IsLogBackupSubCommandOntheCondition(backup, BackupPrepare)
	}
	_, condition := GetBackupCondition(&backup.Status, BackupPrepare)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsVolumeBackupInitialized returns true if volume backup is initialized
func IsVolumeBackupInitialized(backup *Backup) bool {
	if backup.Spec.Mode != BackupModeVolumeSnapshot {
		return false
	}
	_, condition := GetBackupCondition(&backup.Status, VolumeBackupInitialized)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsVolumeBackupInitializeFailed returns true if volume backup is initialized failed
func IsVolumeBackupInitializeFailed(backup *Backup) bool {
	if backup.Spec.Mode != BackupModeVolumeSnapshot {
		return false
	}
	_, condition := GetBackupCondition(&backup.Status, VolumeBackupInitializeFailed)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

func IsVolumeBackupSnapshotsCreated(backup *Backup) bool {
	if backup.Spec.Mode != BackupModeVolumeSnapshot {
		return false
	}
	_, condition := GetBackupCondition(&backup.Status, VolumeBackupSnapshotsCreated)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

func IsVolumeBackupInitializeComplete(backup *Backup) bool {
	if backup.Spec.Mode != BackupModeVolumeSnapshot {
		return false
	}
	_, condition := GetBackupCondition(&backup.Status, VolumeBackupInitializeComplete)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsVolumeBackupComplete returns true if volume backup is complete
func IsVolumeBackupComplete(backup *Backup) bool {
	if backup.Spec.Mode != BackupModeVolumeSnapshot {
		return false
	}
	_, condition := GetBackupCondition(&backup.Status, VolumeBackupComplete)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsVolumeBackupFailed returns true if volume backup is failed
func IsVolumeBackupFailed(backup *Backup) bool {
	if backup.Spec.Mode != BackupModeVolumeSnapshot {
		return false
	}
	_, condition := GetBackupCondition(&backup.Status, VolumeBackupFailed)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsBackupClean returns true if a Backup has been successfully cleaned up
func IsBackupClean(backup *Backup) bool {
	// TODO: now we don't handle fault state, maybe we should consider it in the future
	if backup.Spec.Mode == BackupModeLog && IsLogBackupOnTrack(backup) {
		return false
	}
	if NeedRetainData(backup) {
		return true
	}
	_, condition := GetBackupCondition(&backup.Status, BackupClean)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsBackupCleanFailed returns true if a Backup has failed to clean up
func IsBackupCleanFailed(backup *Backup) bool {
	_, condition := GetBackupCondition(&backup.Status, BackupCleanFailed)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// IsCleanCandidate returns true if a Backup should be added to clean candidate according to cleanPolicy
func IsCleanCandidate(backup *Backup) bool {
	return backup.Spec.Mode == BackupModeLog ||
		backup.Spec.CleanPolicy == CleanPolicyTypeDelete ||
		backup.Spec.CleanPolicy == CleanPolicyTypeOnFailure
}

// NeedRetainData returns true if a Backup need not to be cleaned up according to cleanPolicy
func NeedRetainData(backup *Backup) bool {
	return backup.Spec.CleanPolicy == CleanPolicyTypeRetain ||
		(backup.Spec.CleanPolicy == CleanPolicyTypeOnFailure && !IsBackupFailed(backup))
}

// ParseLogBackupSubcommand parse the log backup subcommand from cr.
func ParseLogBackupSubcommand(backup *Backup) LogSubCommandType {
	if backup.Spec.Mode != BackupModeLog {
		return ""
	}

	var subCommand LogSubCommandType

	switch backup.Spec.LogSubcommand {
	// Users can omit the LogSubcommand field and use the `LogStop` field to stop log backups as in older version.
	case "":
		if backup.Spec.LogStop || IsLogBackupAlreadyStop(backup) {
			subCommand = LogStopCommand
		} else {
			subCommand = LogStartCommand
		}
	case LogStartCommand:
		if IsLogBackupAlreadyPaused(backup) {
			subCommand = LogResumeCommand
		} else {
			subCommand = LogStartCommand
		}
	case LogStopCommand:
		subCommand = LogStopCommand
	case LogPauseCommand:
		subCommand = LogPauseCommand
	default:
		return LogUnknownCommand
	}

	// If the selected subcommand is already sync and logTruncateUntil is set, switch to LogTruncateCommand
	if IsLogSubcommandAlreadySync(backup, subCommand) && backup.Spec.LogTruncateUntil != "" && backup.Spec.LogTruncateUntil != backup.Status.LogSuccessTruncateUntil {
		return LogTruncateCommand
	}

	return subCommand
}

// IsLogSubcommandAlreadySync return whether the log subcommand already sync.
// It only check start/stop/pause subcommand. Truncate subcommand need to check the `logTruncateUntil` separately.
func IsLogSubcommandAlreadySync(backup *Backup, subCommand LogSubCommandType) bool {
	switch subCommand {
	case LogStartCommand:
		return IsLogBackupAlreadyStart(backup)
	case LogStopCommand:
		return IsLogBackupAlreadyStop(backup)
	case LogPauseCommand:
		return IsLogBackupAlreadyPaused(backup)
	case LogResumeCommand:
		return IsLogBackupAlreadyRunning(backup)
	default:
		return false
	}
}

// IsLogBackupSubCommandOntheCondition return whether the log subcommand on the condition.
func IsLogBackupSubCommandOntheCondition(backup *Backup, conditionType BackupConditionType) bool {
	command := ParseLogBackupSubcommand(backup)
	switch command {
	case LogStartCommand, LogStopCommand, LogPauseCommand, LogResumeCommand:
		if subStatus, ok := backup.Status.LogSubCommandStatuses[command]; ok {
			return subStatus.Phase == conditionType
		}
	case LogTruncateCommand:
		// truncate Command's truncating until is the spec truncate until means the truncate is in progress.
		if subStatus, ok := backup.Status.LogSubCommandStatuses[command]; ok {
			return subStatus.LogTruncatingUntil == backup.Spec.LogTruncateUntil && subStatus.Phase == conditionType
		}
	default:
		return false
	}
	return false
}

// GetLogSubcommandConditionInfo gets log subcommand current phase's reason and message
func GetLogSubcommandConditionInfo(backup *Backup) (reason, message string) {
	command := ParseLogBackupSubcommand(backup)
	if subStatus, ok := backup.Status.LogSubCommandStatuses[command]; ok {
		for _, condition := range subStatus.Conditions {
			if subStatus.Phase == condition.Type {
				reason = condition.Reason
				message = condition.Message
				break
			}
		}
	}
	return
}

// IsLogBackupAlreadyStart return whether log backup has already started.
func IsLogBackupAlreadyStart(backup *Backup) bool {
	return backup.Spec.Mode == BackupModeLog && backup.Status.CommitTs != ""
}

// IsLogBackupAlreadyTruncate return whether log backup has already truncated.
func IsLogBackupAlreadyTruncate(backup *Backup) bool {
	if backup.Spec.Mode != BackupModeLog {
		return false
	}
	// spec truncate Until TS <= start commit TS or success truncate until means log backup has been truncated.
	var specTS, successedTS, startCommitTS uint64
	var err error

	specTS, err = config.ParseTSString(backup.Spec.LogTruncateUntil)
	if err != nil {
		return false
	}
	successedTS, _ = config.ParseTSString(backup.Status.LogSuccessTruncateUntil)
	startCommitTS, _ = config.ParseTSString(backup.Status.CommitTs)

	return specTS <= startCommitTS || specTS <= successedTS
}

// IsLogBackupAlreadyStop return whether log backup has already stopped.
func IsLogBackupAlreadyStop(backup *Backup) bool {
	return backup.Spec.Mode == BackupModeLog && backup.Status.Phase == BackupStopped
}

// IsLogBackupOnTrack returns whether log backup is on track.
func IsLogBackupOnTrack(backup *Backup) bool {
	if backup.Spec.Mode != BackupModeLog {
		return false
	}

	switch backup.Status.Phase {
	case BackupScheduled, BackupPrepare, BackupRunning, BackupPaused:
		return true
	default:
		return false
	}
}

// IsLogBackupAlreadyPaused return whether log backup has already paused.
func IsLogBackupAlreadyPaused(backup *Backup) bool {
	return backup.Spec.Mode == BackupModeLog && backup.Status.Phase == BackupPaused
}

// IsLogBackupAlreadyRunning return whether log backup has already resumed.
func IsLogBackupAlreadyRunning(backup *Backup) bool {
	return backup.Spec.Mode == BackupModeLog && backup.Status.Phase == BackupRunning
}
