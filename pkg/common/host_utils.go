/*
Copyright 2019 Hammerspace

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package common

import (
    "bytes"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "strings"
    "time"

    log "github.com/sirupsen/logrus"
    unix "golang.org/x/sys/unix"

    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
    "k8s.io/kubernetes/pkg/util/mount"
)

func execCommandHelper(command string, args...string) ([]byte, error) {
    cmd := exec.Command(command, args...)
    var b bytes.Buffer
    cmd.Stdout = &b
    cmd.Stderr = &b
    if err := cmd.Start(); err != nil {
        log.Error(err)
        return nil, err
    }
    // Wait for the process to finish or kill it after a timeout (whichever happens first):
    done := make(chan error, 1)
    go func() {
        done <- cmd.Wait()
    }()
    select {
    case <-time.After(CommandExecTimeout):
        log.Warnf("Command '%s' with args '%v' did not completed after %d seconds",
            command, args, CommandExecTimeout)
        if err := cmd.Process.Kill(); err != nil {
            log.Error("failed to kill process: ", err)
        }
        return nil, fmt.Errorf("process killed as timeout reached")
    case err := <-done:
        if err != nil {
            log.Errorf("process finished with error = %v", err)
        }
    }
    return b.Bytes(), nil
}

var execCommand = execCommandHelper
// EnsureFreeLoopbackDeviceFile finds the next available loop device under /dev/loop*
// If no free loop devices exist, a new one is created
func EnsureFreeLoopbackDeviceFile() (uint64, error) {
    LOOP_CTL_GET_FREE := uintptr(0x4C82)
    LoopControlPath := "/dev/loop-control"
    ctrl, err := os.OpenFile(LoopControlPath, os.O_RDWR, 0660)
    if err != nil {
        return 0, fmt.Errorf("could not open %s: %v", LoopControlPath, err)
    }
    defer ctrl.Close()
    dev, _, errno := unix.Syscall(unix.SYS_IOCTL, ctrl.Fd(), LOOP_CTL_GET_FREE, 0)
    if dev < 0 {
        return 0, fmt.Errorf("could not get free device: %v", errno)
    }
    return uint64(dev), nil
}

func BindMountDevice(sourcefile, destfile string) error {
    mounter := mount.New("")
    if exists, _ := mounter.ExistsPath(destfile); !exists {
        err := os.MkdirAll(filepath.Dir(destfile), os.FileMode(0644))
        if err == nil {
            err = mounter.MakeFile(destfile)
        }
        if err != nil {
            log.Errorf("could not make destination path for bind mound, %v", err)
            return status.Error(codes.Internal, err.Error())
        }
    }

    err := mounter.Mount(sourcefile, destfile, "", []string{"bind"})
    if err != nil {
        if os.IsPermission(err) {
            return status.Error(codes.PermissionDenied, err.Error())
        }
        if strings.Contains(err.Error(), "invalid argument") {
            return status.Error(codes.InvalidArgument, err.Error())
        }
        return status.Error(codes.Internal, err.Error())
    }
    return nil
}

func GetDeviceMinorNumber(device string) (uint32, error) {
    s := unix.Stat_t{}
    if err := unix.Stat(device, &s); err != nil {
        return 0, err
    }

    dev := uint64(s.Rdev)
    return unix.Minor(dev), nil
}

func MakeEmptyRawFile(pathname string, size int64) error {
    log.Infof("creating file '%s'", pathname)
    sizeStr := strconv.FormatInt(size, 10)
    output, err := execCommand("qemu-img", "create", "-fraw", pathname, sizeStr)
    if err != nil {
        log.Errorf("%s, %v", output, err.Error())
        return err
    }

    return nil
}

func DeleteFile(pathname string) error {
    log.Infof("deleting file '%s'", pathname)
    err := os.Remove(pathname)
    if err != nil {
        return err
    }

    return nil
}

func MountShare(sourcePath, targetPath string, mountFlags []string) error {
    log.Infof("mounting %s to %s, with options %v", sourcePath, targetPath, mountFlags)
    notMnt, err := mount.New("").IsLikelyNotMountPoint(targetPath)
    if err != nil {
        if os.IsNotExist(err) {
            if err := os.MkdirAll(targetPath, 0750); err != nil {
                return status.Error(codes.Internal, err.Error())
            }
            notMnt = true
        } else {
            return status.Error(codes.Internal, err.Error())
        }
    }

    if !notMnt {
        return nil
    }

    mo := mountFlags

    mounter := mount.New("")
    err = mounter.Mount(sourcePath, targetPath, "nfs", mo)
    if err != nil {
        if os.IsPermission(err) {
            return status.Error(codes.PermissionDenied, err.Error())
        }
        if strings.Contains(err.Error(), "invalid argument") {
            return status.Error(codes.InvalidArgument, err.Error())
        }
        return status.Error(codes.Internal, err.Error())
    }

    return nil
}

func determineBackingFileFromLoopDevice(lodevice string) (string, error) {
    output, err := execCommand("losetup", "-a")
    if err != nil {
        return "", status.Errorf(codes.Internal,
            "could not determine backing file for loop device, %v", err)
    }
    devices := strings.Split(string(output), "\n")
    for _, d := range devices {
        if d != "" {
            device := strings.Split(d, " ")
            if lodevice == strings.Trim(device[0], ":()") {
                return strings.Trim(device[len(device)-1], ":()"), nil
            }
        }
    }
    return "", status.Errorf(codes.Internal,
        "could not determine backing file for loop device")
}

func GetNFSExports(address string) ([]string, error) {
    output, err := execCommand("showmount", "--no-headers", "-e", address)
    if err != nil {
        return nil, status.Errorf(codes.Internal,
            "could not determine nfs exports, %v: %s", err, output)
    }
    exports := strings.Split(string(output), "\n")
    toReturn := []string{}
    for _, export := range exports {
        exportTokens := strings.Fields(export)
        if len(exportTokens) > 0 {
            toReturn = append(toReturn, exportTokens[0])
        }
    }
    return toReturn, nil
}

func IsShareMounted(targetPath string) (bool, error) {
    notMnt, err := mount.IsNotMountPoint(mount.New(""), targetPath)

    if err != nil {
        if os.IsNotExist(err) {
            return false, status.Error(codes.NotFound, EmptyTargetPath)
        } else {
            return false, status.Error(codes.Internal, err.Error())
        }
    }
    if notMnt {
        return false, status.Error(codes.NotFound, ShareNotMounted)
    }
    return true, nil
}

func UnmountShare(targetPath string) error {
    mounter := mount.New("")

    isMounted, err := IsShareMounted(targetPath)

    if err != nil {
        log.Error(err.Error())
        return status.Error(codes.Internal, err.Error())
    }
    if !isMounted {
        return status.Error(codes.NotFound, ShareNotMounted)
    }

    err = mounter.Unmount(targetPath)
    if err != nil {
        log.Error(err.Error())
        return status.Error(codes.Internal, err.Error())
    }
    // delete target path
    err = os.Remove(targetPath)
    if err != nil {
        log.Errorf("could not remove target path, %v", err)
        return status.Error(codes.Internal, err.Error())
    }
    return nil
}
