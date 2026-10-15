package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckRejectsDriftWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "plugin.yaml")
	output := filepath.Join(dir, "operands.json")
	if err := os.WriteFile(source, []byte("apiVersion: v1\nkind: Service\nmetadata:\n  name: example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(source, output, false); err != nil {
		t.Fatal(err)
	}
	if err := run(source, output, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(source, output, true); err == nil {
		t.Fatal("check accepted stale operands")
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != "stale" {
		t.Fatal("check modified the generated file")
	}
	if err := run(source, output, false); err != nil {
		t.Fatal(err)
	}
	if err := run(source, output, true); err != nil {
		t.Fatal(err)
	}
}
