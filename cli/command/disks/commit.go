/*
 *  Copyright (c) 2023 NetEase Inc.
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 */

/*
 * Project: CurveAdm
 * Created Date: 2023-02-24
 * Author: Lijin Xiong (lijin.xiong@zstack.io)
 */

package disks

import (
	"strings"

	"github.com/fatih/color"
	"github.com/opencurve/curveadm/cli/cli"
	"github.com/opencurve/curveadm/internal/common"
	comm "github.com/opencurve/curveadm/internal/common"
	"github.com/opencurve/curveadm/internal/configure/disks"
	"github.com/opencurve/curveadm/internal/configure/topology"
	"github.com/opencurve/curveadm/internal/errno"
	"github.com/opencurve/curveadm/internal/storage"
	"github.com/opencurve/curveadm/internal/tui"
	tuicomm "github.com/opencurve/curveadm/internal/tui/common"
	"github.com/opencurve/curveadm/internal/utils"
	"github.com/spf13/cobra"
)

const (
	COMMIT_EXAMPLE = `Examples:
  $ curveadm disks commit /path/to/disks.yaml  # Commit disks`
)

type commitOptions struct {
	filename string
	slient   bool
}

func NewCommitCommand(curveadm *cli.CurveAdm) *cobra.Command {
	var options commitOptions
	cmd := &cobra.Command{
		Use:     "commit DISKS [OPTIONS]",
		Short:   "Commit disks",
		Args:    utils.ExactArgs(1),
		Example: COMMIT_EXAMPLE,
		RunE: func(cmd *cobra.Command, args []string) error {
			options.filename = args[0]
			return runCommit(curveadm, options)
		},
		DisableFlagsInUseLine: true,
	}

	flags := cmd.Flags()
	flags.BoolVarP(&options.slient, "slient", "s", false, "Slient output for disks commit")

	return cmd
}

func readAndCheckDisks(curveadm *cli.CurveAdm, options commitOptions) (string, []*disks.DiskConfig, error) {
	var dcs []*disks.DiskConfig
	// 1) read disks from file
	if !utils.PathExist(options.filename) {
		return "", dcs, errno.ERR_DISKS_FILE_NOT_FOUND.
			F("%s: no such file", utils.AbsPath(options.filename))
	}
	data, err := utils.ReadFile(options.filename)
	if err != nil {
		return data, dcs, errno.ERR_READ_DISKS_FILE_FAILED.E(err)
	}

	// 2) display disks difference
	oldData := curveadm.Disks()
	if !options.slient {
		diff := utils.Diff(oldData, data)
		curveadm.WriteOutln(diff)
	}

	// 3) check disks data
	dcs, err = disks.ParseDisks(data, curveadm)
	return data, dcs, err
}

func assambleNewDiskRecords(dcs []*disks.DiskConfig,
	oldDiskRecords []storage.Disk) ([]storage.Disk, []storage.Disk) {
	keySep := ":"
	newDiskMap := make(map[string]bool)

	var newDiskRecords, diskRecordDeleteList []storage.Disk
	for _, dc := range dcs {
		for _, host := range dc.GetHost() {
			key := strings.Join([]string{host, dc.GetDevice()}, keySep)
			newDiskMap[key] = true
			newDiskRecords = append(
				newDiskRecords, storage.Disk{
					Host:          host,
					Device:        dc.GetDevice(),
					Size:          comm.DISK_DEFAULT_NULL_SIZE,
					URI:           comm.DISK_DEFAULT_NULL_URI,
					MountPoint:    dc.GetMountPoint(),
					FormatPercent: dc.GetFormatPercent(),
					ChunkServerID: comm.DISK_DEFAULT_NULL_CHUNKSERVER_ID,
				})
		}
	}

	for _, dr := range oldDiskRecords {
		key := strings.Join([]string{dr.Host, dr.Device}, keySep)
		if _, ok := newDiskMap[key]; !ok {
			diskRecordDeleteList = append(diskRecordDeleteList, dr)
		}
	}

	return newDiskRecords, diskRecordDeleteList
}

func writeDiskRecord(dr storage.Disk, curveadm *cli.CurveAdm) error {
	if diskRecords, err := curveadm.Storage().GetDisk(
		common.DISK_FILTER_DEVICE, dr.Host, dr.Device); err != nil {
		return err
	} else if len(diskRecords) == 0 {
		if err := curveadm.Storage().SetDisk(
			dr.Host,
			dr.Device,
			dr.MountPoint,
			dr.ContainerImage,
			dr.FormatPercent); err != nil {
			return err
		}
	}
	return nil
}

func syncDiskRecords(data string, dcs []*disks.DiskConfig,
	curveadm *cli.CurveAdm, options commitOptions) error {
	oldDiskRecords := curveadm.DiskRecords()
	tui.SortDiskRecords(oldDiskRecords)

	newDiskRecords, diskRecordDeleteList := assambleNewDiskRecords(dcs, oldDiskRecords)
	tui.SortDiskRecords(newDiskRecords)
	oldDiskRecordsString := tui.FormatDisks(oldDiskRecords)
	newDiskRecordsString := tui.FormatDisks(newDiskRecords)

	if !options.slient {
		diff := utils.Diff(oldDiskRecordsString, newDiskRecordsString)
		curveadm.WriteOutln(diff)
	}

	pass := tuicomm.ConfirmYes("Disk changes are showing above. Do you want to continue?")
	if !pass {
		curveadm.WriteOut(tuicomm.PromptCancelOpetation("commit disk table"))
		return errno.ERR_CANCEL_OPERATION
	}

	// write new disk records
	for _, dr := range newDiskRecords {
		if err := writeDiskRecord(dr, curveadm); err != nil {
			return err
		}
	}

	// delete obsolete disk records
	for _, dr := range diskRecordDeleteList {
		if dr.ChunkServerID != comm.DISK_DEFAULT_NULL_CHUNKSERVER_ID {
			return errno.ERR_DELETE_SERVICE_BINDING_DISK.
				F("The disk[%s:%s] is used by service[%s:%s]",
					dr.Host, dr.Device, topology.ROLE_CHUNKSERVER, dr.ChunkServerID)
		}

		if err := curveadm.Storage().DeleteDisk(dr.Host, dr.Device); err != nil {
			return errno.ERR_UPDATE_DISK_FAILED.E(err)
		}
	}

	return nil
}

func runCommit(curveadm *cli.CurveAdm, options commitOptions) error {
	// 1) read and check disks
	data, dcs, err := readAndCheckDisks(curveadm, options)
	if err != nil {
		return err
	}

	// 2) confirm by user
	pass := tuicomm.ConfirmYes("Do you want to continue?")
	if !pass {
		curveadm.WriteOut(tuicomm.PromptCancelOpetation("commit disks"))
		return errno.ERR_CANCEL_OPERATION
	}

	// 3) add disk records
	err = syncDiskRecords(data, dcs, curveadm, options)
	if err != nil {
		return err
	}

	// 4) add disks data
	err = curveadm.Storage().SetDisks(data)
	if err != nil {
		return errno.ERR_UPDATE_DISKS_FAILED.
			F("commit disks failed")
	}

	// 5) print success prompt
	curveadm.WriteOutln(color.GreenString("Disks updated"))
	return nil
}
