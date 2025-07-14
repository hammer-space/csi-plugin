CSI Volume Unmount Recovery Guide
Volume Unmount Recovery Guide
In certain scenarios (e.g., when a pod is force deleted or a process is still holding a file), the CSI driver may fail to unmount a volume with an error like device or resource busy. This can lead to pods stuck in Terminating, volumes stuck in Released, and failed cleanup operations.

If this occurs even after the CSI driver retries unmounting, follow the steps below to manually recover the node.

## 🧹 Manual Cleanup Steps (Run on Affected Node)
Find the Mount Path
```
findmnt | grep <volume-id> # Find Processes Holding the Mount
```

```
lsof +f -- <mount-path>
```
## or
```
fuser -vm <mount-path> # Kill the Process
```
```
kill -15 <pid>
```
## If needed:
```
kill -9 <pid>
```

## Retry Unmount
```
umount <mount-path>
umount -l <mount-path>     # Lazy unmount (if regular fails)
umount -f <mount-path>     # Force unmount (only for NFS/FUSE)
```
## Clean up Mount Directory

```
rm -rf <mount-path>
```

## Restart Kubelet (Optional, Last Resort)
```
systemctl restart kubelet
```

For recurring issues or uncertainty, please contact Hammerspace support.