
⚙️ Tunable Retry Parameters
You can configure retry behavior in the CSI driver to handle transient EBUSY issues:

```
unmountRetryCount: 5          # default
unmountRetryInterval: 1s      # default
```

```
apiVersion: v1
kind: ConfigMap
metadata:
  name: csi-env-config
  namespace: kube-system
data:
  UNMOUNT_RETRY_COUNT: "5"
  UNMOUNT_RETRY_INTERVAL: "1s"

```
These settings can be updated via the driver config or environment variables (if supported) to better match your environment (e.g., NFS, slow shutdowns, containerd delays).

## 📎 Notes
```
Avoid using umount -f on block devices unless you know the volume is stateless.
```

Always attempt graceful unmount first.

Monitor CSI logs to detect retry failures early.

For recurring issues or uncertainty, please contact Hammerspace support.