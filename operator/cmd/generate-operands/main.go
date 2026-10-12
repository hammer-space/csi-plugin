// generate-operands must run from operator/, as the Makefile and Dockerfile do.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/hammer-space/csi-plugin/operator/internal/manifests"
)

func main() {
	source := flag.String("source", "../deploy/kubernetes/kubernetes-1.36/plugin.yaml", "canonical CSI manifest")
	output := flag.String("output", "internal/operator/operands.json", "generated embedded template")
	imagesOutput := flag.String("images-output", "config/development-images.json", "generated development image inventory")
	manager := flag.String("manager", "config/manager.yaml", "manager manifest whose image defaults are synchronized")
	rbac := flag.String("rbac", "config/rbac.yaml", "operator RBAC that must cover every operand ClusterRole rule")
	check := flag.Bool("check", false, "fail if the embedded template is stale, without changing files")
	flag.Parse()
	if err := run(*source, *output, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := syncImages(*output, *imagesOutput, *manager, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Runs on every generate, build, test, and image build, so a driver RBAC
	// change in plugin.yaml cannot ship without the matching rbac.yaml grant.
	if err := checkRBAC(*output, *rbac); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func checkRBAC(operands, rbac string) error {
	data, err := os.ReadFile(operands)
	if err != nil {
		return err
	}
	f, err := os.Open(rbac)
	if err != nil {
		return err
	}
	defer f.Close()
	return manifests.CheckDelegatedRBAC(data, f)
}

func syncImages(operands, imagesOutput, manager string, check bool) error {
	data, err := os.ReadFile(operands)
	if err != nil {
		return err
	}
	images, err := manifests.ImageDefaults(data)
	if err != nil {
		return err
	}
	inventory, err := json.MarshalIndent(images, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.Open(manager)
	if err != nil {
		return err
	}
	defer f.Close()
	deployment, err := manifests.ManagerImages(f, images)
	if err != nil {
		return err
	}
	for _, file := range []struct {
		path string
		data []byte
	}{
		{imagesOutput, append(inventory, '\n')}, {manager, deployment},
	} {
		if err := writeGenerated(file.path, file.data, check); err != nil {
			return err
		}
	}
	return nil
}

func run(source, output string, check bool) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := manifests.Generate(f)
	if err != nil {
		return err
	}
	return writeGenerated(output, data, check)
}

func writeGenerated(output string, data []byte, check bool) error {
	previous, err := os.ReadFile(output)
	if err == nil && bytes.Equal(previous, data) {
		return nil
	}
	if check {
		return fmt.Errorf("%s is stale; run make -C operator generate from the repository root", output)
	}
	return os.WriteFile(output, data, 0644)
}
