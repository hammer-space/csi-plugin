package sanitytest

import (
	"crypto/rand"
	"fmt"
	"github.com/kubernetes-csi/csi-test/pkg/sanity"
	yaml "gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
)

func createMountTargetLocation(targetPath string) error {
	fileInfo, err := os.Stat(targetPath)
	if err != nil && os.IsNotExist(err) {
		return os.MkdirAll(targetPath, 0755)
	} else if err != nil {
		return err
	}
	if !fileInfo.IsDir() {
		return fmt.Errorf("Target location %s is not a directory", targetPath)
	}

	return nil
}

func loadSecrets(path string) (*sanity.CSISecrets, error) {
	var creds sanity.CSISecrets

	yamlFile, err := ioutil.ReadFile(path)
	if err != nil {
		return &creds, fmt.Errorf("failed to read file %q: #%v", path, err)
	}

	err = yaml.Unmarshal(yamlFile, &creds)
	if err != nil {
		return &creds, fmt.Errorf("error unmarshaling yaml: #%v", err)
	}

	return &creds, nil
}

var uniqueSuffix = "-" + pseudoUUID()

// pseudoUUID returns a unique string generated from random
// bytes, empty string in case of error.
func pseudoUUID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Shouldn't happen?!
		return ""
	}
	return fmt.Sprintf("%08X-%08X", b[0:4], b[4:8])
}

// uniqueString returns a unique string by appending a random
// number. In case of an error, just the prefix is returned, so it
// alone should already be fairly unique.
func uniqueString(prefix string) string {
	return prefix + uniqueSuffix
}
