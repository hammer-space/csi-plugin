package common

import (
    "errors"
    "fmt"
    "path"
    "strings"
)

func GetSnapshotNameFromSnapshotId(snapshotId string) (string, error) {
    tokens := strings.SplitN(snapshotId, "|", 2)
    if len(tokens) != 2 {
        return "", errors.New(fmt.Sprintf(ImproperlyFormattedSnapshotId, snapshotId))
    }
    return tokens[0], nil
}

func GetShareNameFromSnapshotId(snapshotId string) (string, error) {
    tokens := strings.SplitN(snapshotId, "|", 2)
    if len(tokens) != 2 {
        return "", errors.New(fmt.Sprintf(ImproperlyFormattedSnapshotId, snapshotId))
    }
    return path.Base(tokens[1]), nil
}