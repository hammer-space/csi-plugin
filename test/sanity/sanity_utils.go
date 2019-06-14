package sanitytest

import (
	"crypto/rand"
	"fmt"
	"github.com/hammer-space/csi-plugin/pkg/client"
	"github.com/kubernetes-csi/csi-test/pkg/sanity"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
)


func copyStringMap(originalMap map[string]string) map[string]string {
	newMap := make(map[string]string)
	for key, value := range originalMap {
		newMap[key] = value
	}
	return newMap
}

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

func GetHammerspaceClient() (*client.HammerspaceClient){
	tlsVerify, _ := strconv.ParseBool(os.Getenv("HS_TLS_VERIFY"))

	client, err := client.NewHammerspaceClient(
		os.Getenv("HS_ENDPOINT"),
		os.Getenv("HS_USERNAME"),
		os.Getenv("HS_PASSWORD"),
		tlsVerify)
	if err != nil {
		os.Exit(1)
	}
	return client
}

func parseMetadataTagsParam(additionalMetadataTagsString string) (map[string]string){

	additionalMetadataTags := map[string]string{}
	tagsList := strings.Split(additionalMetadataTagsString, ",")
	for _, m := range tagsList {
		extendedInfo := strings.Split(m, "=")
		//assert options is len 2
		key := strings.TrimSpace(extendedInfo[0])
		value := strings.TrimSpace(extendedInfo[1])

		additionalMetadataTags[key] = value
	}

	return additionalMetadataTags
}