# Static PV Multi-Site Mounting (Same Share, Different Clusters)

This document explains a safe, repeatable workflow for mounting the same Hammerspace share from two sites using **static PVs**, while keeping CSI `volumeHandle` values unique.

## Why This Matters

- CSI requires `volumeHandle` (CSI `VolumeId`) to be **globally unique per driver**.
- This driver also uses `volumeHandle` to decide **what path to mount**.
- A mismatch can lead to:
  - staging path hash collisions
  - lock timeouts
  - wrong mount targets

## Driver Behavior Summary

When `mountBackingShareName` is set:
- The driver mounts the backing share.
- It then bind-mounts a **subpath inside the share**.
- Default subpath = `volumeHandle` (historical behavior).

When `mountBackingShareName` is not set:
- The driver treats `volumeHandle` as the **share path itself**.
- There is **no subpath override** in this mode.

## New Optional Attribute (Static PVs)

You can now set an explicit subpath using:
- `shareSubPath` (preferred)
- `exportSubPath` or `mountSubPath` (aliases)

If set, the driver mounts:
`/tmp/<mountBackingShareName>/<shareSubPath>`

If `shareSubPath` is `.` or empty, it mounts the **share root**.

This is backward compatible. If the attribute is not set, behavior is unchanged.

## Recommended Workflow (Same Share, Two Sites)

Use unique `volumeHandle` values, and explicitly mount the share root via `shareSubPath`.

Example:

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: hs-pv-aiml-site1
spec:
  csi:
    driver: com.hammerspace.csi
    volumeHandle: aiml-site1
    volumeAttributes:
      mountBackingShareName: "aiml"
      shareSubPath: "."
```

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: hs-pv-aiml-site2
spec:
  csi:
    driver: com.hammerspace.csi
    volumeHandle: aiml-site2
    volumeAttributes:
      mountBackingShareName: "aiml"
      shareSubPath: "."
```

Both PVs will mount the **same share root**, but the handles remain unique.

## Common Misconfigurations

- Using the same `volumeHandle` for both PVs:
  - This violates CSI uniqueness and causes staging/lock collisions.
- Setting `mountBackingShareName` but not creating the subpath:
  - If you rely on default behavior, `volumeHandle` must exist as a directory inside the share.
- Trying to use subpath overrides without `mountBackingShareName`:
  - Not supported in the current driver path.

## Troubleshooting Checklist

1. Ensure `volumeHandle` values are unique.
2. If `mountBackingShareName` is set, check the share is mounted at `/tmp/<share>`.
3. If using `shareSubPath`, verify the directory exists inside the share.
4. Look for lock timeout logs tied to duplicate `volumeHandle`.
