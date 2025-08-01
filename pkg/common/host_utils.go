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
	"context"
	"errors"
	"fmt"
	"net"
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
	"k8s.io/mount-utils"
)

const LOOP_CTL_GET_FREE = 0x4C82

var (
	defaultMountCheckTimeout time.Duration = 50 * time.Second // Default timeout for checking mount status
)

func init() {
	// Read environment variables for mount check timeout
	mountCheckTimeoutStr := os.Getenv("MOUNT_CHECK_TIMEOUT")
	if mountCheckTimeoutStr != "" {
		if timeout, err := time.ParseDuration(mountCheckTimeoutStr); err == nil && timeout > 0 {
			defaultMountCheckTimeout = timeout
		} else {
			log.Warnf("Invalid MOUNT_CHECK_TIMEOUT=%s; using default %s", mountCheckTimeoutStr, defaultMountCheckTimeout)
		}
	}

	log.Infof("mountCheckTimeout=%s", defaultMountCheckTimeout)
}

func execCommandHelper(command string, args ...string) ([]byte, error) {
	cmd := exec.Command(command, args...)
	log.Debugf("Executing command: %v", cmd)
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
			return nil, err
		}
	}
	return b.Bytes(), nil
}

var ExecCommand = execCommandHelper

// EnsureFreeLoopbackDeviceFile finds the next available loop device under /dev/loop*
// If no free loop devices exist, a new one is created
func EnsureFreeLoopbackDeviceFile() (uint64, error) {
	loopControlPath := "/dev/loop-control"

	// Open the loop-control device
	ctrl, err := os.OpenFile(loopControlPath, os.O_RDWR, 0660)
	if err != nil {
		return 0, fmt.Errorf("could not open %s: %w", loopControlPath, err)
	}
	defer ctrl.Close()

	// Use IoctlGetInt to get a free loop device
	dev, err := unix.IoctlGetInt(int(ctrl.Fd()), LOOP_CTL_GET_FREE)
	if err != nil {
		return 0, fmt.Errorf("could not get free loop device: %w", err)
	}
	return uint64(dev), nil
}

func MountFilesystem(sourcefile, destfile, fsType string, mountFlags []string) error {
	mounter := mount.New("")
	// Check if the file already exists
	if _, err := os.Stat(destfile); os.IsNotExist(err) {
		// Make sure parent dir exists
		err := os.MkdirAll(filepath.Dir(destfile), 0755) // Use 0755 for dirs, not 0644
		if err != nil {
			log.Errorf("could not create parent directory: %v", err)
			return status.Error(codes.Internal, err.Error())
		}

		// Create the file
		f, err := os.OpenFile(destfile, os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			log.Errorf("could not create target file: %v", err)
			return status.Error(codes.Internal, err.Error())
		}
		f.Close()
	}

	err := mounter.Mount(sourcefile, destfile, fsType, mountFlags)
	if err != nil {
		if os.IsPermission(err) {
			return status.Error(codes.PermissionDenied, err.Error())
		}
		if strings.Contains(err.Error(), "Invalid argument") {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		return status.Error(codes.Internal, err.Error())
	}
	return nil
}

func ExpandFilesystem(device, fsType string) error {
	log.Infof("Resizing filesystem on file '%s' with '%s' filesystem", device, fsType)

	var command string
	if fsType == "xfs" {
		command = "xfs_growfs"
	} else {
		command = "resize2fs"
	}
	output, err := ExecCommand(command, device)
	if err != nil {
		log.Errorf("Could not expand filesystem on device %s: %s: %s", device, err.Error(), output)
		return err
	}
	return nil
}

func BindMountDevice(sourcefile, destfile string) error {
	mounter := mount.New("")
	// Check if the file already exists
	if _, err := os.Stat(destfile); os.IsNotExist(err) {
		// Make sure parent dir exists
		err := os.MkdirAll(filepath.Dir(destfile), 0755) // Use 0755 for dirs, not 0644
		if err != nil {
			log.Errorf("could not create parent directory: %v", err)
			return status.Error(codes.Internal, err.Error())
		}

		// Create the file
		f, err := os.OpenFile(destfile, os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			log.Errorf("could not create target file: %v", err)
			return status.Error(codes.Internal, err.Error())
		}
		f.Close()
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
	output, err := ExecCommand("qemu-img", "create", "-fraw", pathname, sizeStr)
	if err != nil {
		log.Errorf("%s, %v", output, err.Error())
		return err
	}
	return nil
}

func ExpandDeviceFileSize(pathname string, size int64) error {
	log.Infof("resizing device file '%s'", pathname)
	sizeStr := strconv.FormatInt(size, 10)
	loopdev, err := determineLoopDeviceFromBackingFile(pathname)
	if err != nil {
		// log.Errorf("DFERR: loopdev: '%s', error: '%v'", loopdev, err.Error())
		return err
	}
	// Refresh the loop device size with losetup -c
	// Requires UBI image
	loresize, err := ExecCommand("losetup", "-c", loopdev)
	if err != nil {
		log.Errorf("Resizing loop device '%s' failed with output '%s': '%v'", loopdev, loresize, err.Error())
		return err
	}
	output, err := ExecCommand("qemu-img", "resize", "-fraw", pathname, sizeStr)
	if err != nil {
		log.Errorf("%s, %v", output, err.Error())
		return err
	}
	return nil
}

func FormatDevice(device, fsType string) error {
	log.Infof("formatting file '%s' with '%s' filesystem", device, fsType)
	args := []string{device}
	if fsType == "xfs" {
		args = []string{"-m", "reflink=0", device}
	}
	output, err := ExecCommand(fmt.Sprintf("mkfs.%s", fsType), args...)
	if err != nil {
		log.Errorf("Error executing mkfs command. %v", err)
		if output != nil && strings.Contains(string(output), "will not make a filesystem here") {
			log.Warningf("Device %s is already mounted", device)
			return err
		}
		log.Errorf("Could not format device %s: %s", device, err.Error())
		return err
	}
	return nil
}

func DeleteFile(pathname string) error {
	log.Infof("deleting file '%s'", pathname)

	// Check if the file exists
	if _, err := os.Stat(pathname); err != nil {
		// If the file does not exist, return without an error
		if os.IsNotExist(err) {
			log.Errorf("file '%s' does not exist", pathname)
			return nil
		}
		// If there was an error other than the file not existing, return it
		return err
	}

	// Delete the file
	if err := os.Remove(pathname); err != nil {
		return err
	}

	return nil
}

func MountShare(sourcePath, targetPath string, mountFlags []string) error {
	log.Infof("mounting %s to %s, with options %v", sourcePath, targetPath, mountFlags)
	notMnt, err := SafeIsLikelyNotMountPoint(targetPath)
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
	output, err := ExecCommand("losetup", "-a")
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

// Note that this function does not work in Alpine image due to
// losetup cutting the output off at 79 characters
func determineLoopDeviceFromBackingFile(backingfile string) (string, error) {
	log.Infof("determine loop device from backing file: '%s'", backingfile)
	output, err := ExecCommand("losetup", "-a")
	if err != nil {
		return "", status.Errorf(codes.Internal,
			"could not determine loop device for backing file, %v", err)
	}
	devices := strings.Split(string(output), "\n")
	for _, d := range devices {
		if d != "" {
			device := strings.Split(d, " ")
			if backingfile == strings.Trim(device[2], ":()") {
				log.Infof("matched loop dev: '%s'", strings.Trim(device[0], ":()"))
				return strings.Trim(device[0], ":()"), nil
			}
		}
	}
	return "", status.Errorf(codes.Internal,
		"could not determine loop device for backing file")
}

func GetNFSExports(address string) ([]string, error) {
	// Create a context with timeout of 30 seconds
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Execute the command within the context
	outputChan := make(chan []byte)
	errChan := make(chan error)
	go func() {
		output, err := ExecCommand("showmount", "--no-headers", "-e", address)
		if err != nil {
			errChan <- err
			return
		}
		outputChan <- output
	}()

	select {
	case <-ctx.Done():
		// Timeout exceeded
		return nil, status.Errorf(codes.DeadlineExceeded, "command execution timed out")
	case err := <-errChan:
		return nil, status.Errorf(codes.Internal, "could not determine nfs exports: %v", err)
	case output := <-outputChan:
		exports := strings.Split(string(output), "\n")
		toReturn := []string{}
		for _, export := range exports {
			exportTokens := strings.Fields(export)
			if len(exportTokens) > 0 {
				toReturn = append(toReturn, exportTokens[0])
			}
		}
		if len(toReturn) == 0 {
			return nil, status.Errorf(codes.Internal, "could not determine nfs exports")
		}
		return toReturn, nil
	}
}

func computeUaddr(ipAddress string, port int) (string, string, error) {
	ipType, err := checkIPType(ipAddress)
	if err != nil {
		return "", "", err
	}

	switch ipType {
	case "IPv4":
		return computeIPv4Uaddr(ipAddress, port), "tcp", nil
	case "IPv6":
		return computeIPv6Uaddr(ipAddress, port), "tcp", nil
	default:
		return "", "", errors.New("unsupported IP type")
	}
}

func computeIPv4Uaddr(ipAddress string, port int) string {
	// Split the IPv4 address into octets
	octets := strings.Split(ipAddress, ".")
	if len(octets) != 4 {
		return ""
	}

	// Convert port to hexadecimal and get the last two digits
	portHex := strconv.FormatInt(int64(port), 16)
	portHex = fmt.Sprintf("%04s", portHex) // pad with zeros if necessary
	portHigh, _ := strconv.ParseInt(portHex[:2], 16, 0)
	portLow, _ := strconv.ParseInt(portHex[2:], 16, 0)

	// Compute the final uaddr string for IPv4
	uaddr := fmt.Sprintf("%s.%d.%d", ipAddress, portHigh, portLow)
	return uaddr
}

func computeIPv6Uaddr(ipAddress string, port int) string {
	// Convert port to hexadecimal and format it
	portHex := fmt.Sprintf("%04x", port)

	// Compute the final uaddr string for IPv6
	uaddr := fmt.Sprintf("[%s]:%s", ipAddress, portHex)
	return uaddr
}

func checkIPType(ipAddress string) (string, error) {
	ip := net.ParseIP(ipAddress)
	if ip == nil {
		return "", errors.New("invalid IP address")
	}
	if ip.To4() != nil {
		return "IPv4", nil
	} else if ip.To16() != nil {
		return "IPv6", nil
	}
	return "", errors.New("unknown IP type")
}

func CheckNFSExports(address string) (bool, error) {
	// Create a context with timeout of 30 seconds
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Infof("Checking floating ip %s", address)

	uaddr, protocol, err := computeUaddr(address, 2049)
	if err != nil {
		log.Errorf("Error while computing uaddr: %v", err)
	}

	// Execute the command within the context
	outputChan := make(chan []byte)
	errChan := make(chan error)
	go func() {
		output, err := ExecCommand("rpcinfo", "-a", uaddr, "-T", protocol, "100003", "3")
		if err != nil {
			errChan <- err
			return
		}
		log.Infof("Check was success on uaddr %s, with protocol %s.", uaddr, protocol)
		outputChan <- output
	}()

	select {
	case <-ctx.Done():
		// Timeout exceeded
		return false, status.Errorf(codes.DeadlineExceeded, "command execution timed out while checking nfs exports with rpcinfo")
	case err := <-errChan:
		return false, status.Errorf(codes.Internal, "could not determine nfs exports: %v", err)
	case output := <-outputChan:
		log.Infof("%s", string(output))
		return true, nil
	}
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
		return false, nil
	}
	return true, nil
}

func UnmountFilesystem(targetPath string) error {
	mounter := mount.New("")

	isMounted, err := IsShareMounted(targetPath)

	if err != nil {
		log.Error(err.Error())
		return status.Error(codes.Internal, err.Error())
	}
	if !isMounted {
		return nil
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

func SetMetadataTags(localPath string, tags map[string]string) error {
	// hs attribute set localpath -e "CSI_DETAILS_TABLE{'<version-string>','<plugin-name-string>','<plugin-version-string>','<plugin-git-hash-string>'}"
	attributeSetOutput, err := ExecCommand("hs",
		"attribute",
		"set", "CSI_DETAILS",
		fmt.Sprintf("-e \"CSI_DETAILS_TABLE{'%s','%s','%s','%s'}\" ", CsiVersion, CsiPluginName, Version, Githash),
		localPath)
	if err != nil {
		log.Errorf("Failed to set CSI_DETAILS metadata. Command output %s. Error %s", string(attributeSetOutput), err.Error())
	}

	log.Debugf("hs attributes set. Command output %s", string(attributeSetOutput))

	for tag_key, tag_value := range tags {
		output, err := ExecCommand("hs", "-v", "tag", "set", "-e", tag_value, tag_key, localPath)

		// FIXME: The HS client returns exit code 0 even on failure, so we can't detect errors
		if err != nil {
			log.Errorf("%s", "Failed to set tag. Error - %v"+err.Error())
			break
		}
		log.Debugf("hs tag set. output: %s", output)
	}

	return err
}

// resolveFQDN resolves the FQDN to an IP address
func ResolveFQDN(fqdn string) (string, error) {
	if fqdn == "" {
		return "", errors.New("FQDN is empty")
	}
	ips, err := net.LookupIP(fqdn)
	if err != nil {
		return "", fmt.Errorf("failed to resolve FQDN %s: %v", fqdn, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no IP addresses found for FQDN %s", fqdn)
	}
	// Use the first resolved IP address
	return ips[0].String(), nil
}

// Wrapper function to check mount status safely
func SafeIsLikelyNotMountPoint(path string) (bool, error) {
	type result struct {
		notMnt bool
		err    error
	}

	resultChan := make(chan result, 1)
	// Use provided timeout if set, otherwise default to 1 minute
	to := defaultMountCheckTimeout
	go func() {
		notMnt, err := mount.New("").IsLikelyNotMountPoint(path)
		resultChan <- result{notMnt: notMnt, err: err}
	}()

	select {
	case res := <-resultChan:
		return res.notMnt, res.err
	case <-time.After(to):
		return false, context.DeadlineExceeded
	}
}

// MakeEmptyRawFolder creates a folder at the specified path
func MakeEmptyRawFolder(pathname string) error {
	log.Debugf("checking folder '%s'", pathname)

	// Check if directory exists
	info, err := os.Stat(pathname)
	if err == nil {
		if !info.IsDir() {
			log.Errorf("Path exists but is not a directory: %s", pathname)
			return status.Error(codes.Internal, "path exists but is not a directory")
		}
		// Correct permissions if needed
		err = os.Chmod(pathname, os.FileMode(0755))
		if err != nil {
			log.Errorf("Failed to set correct permissions on %s: %v", pathname, err)
			return status.Error(codes.Internal, err.Error())
		}
		log.Debugf("Directory already exists: %s", pathname)
		return nil
	}

	// Create the directory if it does not exist
	if os.IsNotExist(err) {
		log.Debugf("Creating folder with path as -> %s", pathname)
		err = os.MkdirAll(pathname, os.FileMode(0755))
		if err != nil {
			log.Errorf("could not make folder, %v", err)
			return status.Error(codes.Internal, err.Error())
		}
		log.Debugf("Successfully created folder: %s", pathname)
		return nil
	}

	// Handle unexpected errors
	log.Errorf("Unexpected error checking folder: %v", err)
	return status.Error(codes.Internal, err.Error())
}
