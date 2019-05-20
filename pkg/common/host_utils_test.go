package common

import (
    "testing"
    "reflect"
)

func TestGetNFSExports(t *testing.T) {
    execCommand = func(command string, args...string) ([]byte, error) {
        return []byte(""), nil
    }
    expected := []string{}
    actual, err := GetNFSExports("127.0.0.1")
    if err != nil {
        t.Logf("Unexpected error, %v", err)
        t.FailNow()
    }
    if !reflect.DeepEqual(actual, expected) {
        t.Logf("Expected: %v", expected)
        t.Logf("Actual: %v", actual)
        t.FailNow()
    }

    execCommand = func(command string, args...string) ([]byte, error) {
        return []byte(`


`), nil
    }
    expected = []string{}
    actual, err = GetNFSExports("127.0.0.1")
    if err != nil {
        t.Logf("Unexpected error, %v", err)
        t.FailNow()
    }
    if !reflect.DeepEqual(actual, expected) {
        t.Logf("Expected: %v", expected)
        t.Logf("Actual: %v", actual)
        t.FailNow()
    }

    execCommand = func(command string, args...string) ([]byte, error) {
        return []byte(`/test    *
/mnt/data-portal/test        *
/hs/test				*
`), nil
    }
    expected = []string{"/test", "/mnt/data-portal/test", "/hs/test"}
    actual, err = GetNFSExports("127.0.0.1")
    if err != nil {
        t.Logf("Unexpected error, %v", err)
        t.FailNow()
    }
    if !reflect.DeepEqual(actual, expected) {
        t.Logf("Expected: %v", expected)
        t.Logf("Actual: %v", actual)
        t.FailNow()
    }
}


func TestDetermineBackingFileFromLoopDevice(t *testing.T) {
    execCommand = func(command string, args ...string) ([]byte, error) {
        return []byte(`
/dev/loop0: 0 /tmp/test
/dev/loop1: 0 /tmp/test
/dev/loop2: 0 /tmp//test-csi-block/sanity-node-full-E067A84C-D67CAA8E
`), nil
    }
    expected := "/tmp/test"
    actual, err := determineBackingFileFromLoopDevice("/dev/loop0")
    if err != nil {
        t.Logf("Unexpected error, %v", err)
        t.FailNow()
    }
    if !reflect.DeepEqual(actual, expected) {
        t.Logf("Expected: %v", expected)
        t.Logf("Actual: %v", actual)
        t.FailNow()
    }
}

func TestExecCommandHelper(t *testing.T) {
    expected := []byte("test\n")
    actual, err := execCommandHelper("echo", "test")
    if err != nil {
        t.Logf("Unexpected error, %v", err)
        t.FailNow()
    }
    if !reflect.DeepEqual(actual, expected) {
        t.Logf("Expected: %v", expected)
        t.Logf("Actual: %v", actual)
        t.FailNow()
    }

    CommandExecTimeout = 1
    _, err = execCommandHelper("sleep", "5")
    if err == nil {
        t.Logf("Expected error")
        t.FailNow()
    }

}