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

// Package client provides an http client for interacting with the Hammerspace API
package client

import (
    "bytes"
    "crypto/tls"
    "encoding/json"
    "errors"
    "fmt"
    "io/ioutil"
    "net/http"
    "net/http/cookiejar"
    "net/url"
    "os"
    "path"
    "strconv"
    "strings"
    "time"

    log "github.com/sirupsen/logrus"
    "golang.org/x/net/publicsuffix"

    "github.com/hammer-space/csi-plugin/pkg/common"
    "github.com/jpillora/backoff"
)

const (
    BasePath            = "/mgmt/v1.2/rest"
    taskPollTimeout     = 3600 * time.Second // Seconds
    taskPollIntervalCap = 30 * time.Second   //Seconds, The maximum duration between calls when polling task objects
)

type HammerspaceClient struct {
    username   string
    password   string
    endpoint   string
    httpclient *http.Client
}

func NewHammerspaceClient(endpoint, username, password string, tlsVerify bool) (*HammerspaceClient, error) {
    jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
    if err != nil {
        log.Error(err)
        return nil, err
    }
    tr := &http.Transport{
        MaxIdleConns:       10,
        IdleConnTimeout:    30 * time.Second,
        DisableCompression: true,
        TLSClientConfig:    &tls.Config{InsecureSkipVerify: !tlsVerify},
    }
    httpclient := &http.Client{
        Transport: tr,
        Jar:       jar,
    }
    hsclient := &HammerspaceClient{
        username:   username,
        password:   password,
        endpoint:   endpoint,
        httpclient: httpclient,
    }

    err = hsclient.EnsureLogin()

    return hsclient, err
}

// GetAnvilPortal returns the hostname of the configured Hammerspace API gateway
func (client *HammerspaceClient) GetAnvilPortal() (string, error) {
    endpointUrl, _ := url.Parse(client.endpoint)

    return endpointUrl.Hostname(), nil
}

// Return a string with a floating data portal IP
func (client *HammerspaceClient) GetPortalFloatingIp() (string, error) {
// Instead of using /cntl, use /cntl/state to simplify processing of the JSON
// struct. If using /cntl, add [] before cluster struct
  req, err := client.generateRequest("GET", "/cntl/state", "")
  statusCode, respBody, _, err := client.doRequest(*req)

  if err != nil {
      return "", err
  }
  if statusCode != 200 {
      return "", errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
  }
  var clusters common.Cluster
  err = json.Unmarshal([]byte(respBody), &clusters)
  if err != nil {
      log.Error("Error parsing JSON response: " + err.Error())
      return "", err
  }
  // Local random function
  random_select := func () bool {
    r := make(chan struct{})
    close(r)
    select {
    case <-r:
        return false
    case <-r:
        return true
    }
  }
  floatingip := ""
  for _, p := range clusters.PortalFloatingIps {
    floatingip = p.Address
    // If there are more than 1 floating IPs configured, randomly select one
    rr := random_select()
    if rr == true {
      break
    }
  }
  return floatingip, nil
}

// GetDataPortals returns a list of operational data-portals
// those with a matching nodeID are put at the top of the list
func (client *HammerspaceClient) GetDataPortals(nodeID string) ([]common.DataPortal, error) {
    req, err := client.generateRequest("GET", "/data-portals/", "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return nil, err
    }
    if statusCode != 200 {
        return nil, errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }

    var portals []common.DataPortal
    err = json.Unmarshal([]byte(respBody), &portals)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
        return nil, err
    }

    // filter dataportals
    var filteredPortals []common.DataPortal
    for _, p := range portals {
        if p.OperState == "UP" && p.AdminState == "UP" && p.DataPortalType == "NFS_V3" {
            filteredPortals = append(filteredPortals, p)
        }
    }

    // sort dataportals
    var colocatedPortals []common.DataPortal
    var otherPortals []common.DataPortal
    //// Find colocated node portals
    for _, p := range filteredPortals {
        if p.Node.Name == nodeID {
            colocatedPortals = append(colocatedPortals, p)
            log.Infof("Found co-located data-portal, %s, with node name, %s", p.Uoid["uuid"], p.Node.Name)
        } else {
            otherPortals = append(otherPortals, p)
        }
    }

    sortedPortals := append(colocatedPortals, otherPortals...)

    return sortedPortals, nil
}

// Logs into Hammerspace Anvil Server
func (client *HammerspaceClient) EnsureLogin() error {
    resp, err := client.httpclient.PostForm(fmt.Sprintf("%s%s/login", client.endpoint, BasePath),
        url.Values{"username": {client.username},
            "password": {client.password}})
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    body, err := ioutil.ReadAll(resp.Body)
    bodyString := string(body)
    responseLog := log.WithFields(log.Fields{
        "statusCode": resp.StatusCode,
        "body":       bodyString,
        "headers":    resp.Header,
        "url":        resp.Request.URL,
    })

    if err != nil {
        log.Error(err)
    }
    if resp.StatusCode != 200 {
        err = errors.New("failed to login to Hammerspace Anvil")
        responseLog.Error(err)
    }
    return err
}

func (client *HammerspaceClient) doRequest(req http.Request) (int, string, map[string][]string, error) {
    log.Debugf("sending request %s %s", req.Method, req.URL)

    resp, err := client.httpclient.Do(&req)
    // Attempt to login
    if err == nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
        client.EnsureLogin()
        resp, err = client.httpclient.Do(&req)
    }
    if err != nil {
        return 0, "", nil, err
    }
    defer resp.Body.Close()
    body, err := ioutil.ReadAll(resp.Body)
    bodyString := string(body)
    responseLog := log.WithFields(log.Fields{
        "statusCode":  resp.StatusCode,
        "body":        bodyString,
        "headers":     resp.Header,
        "request_url": req.URL,
    })
    if resp.StatusCode >= 500 {
        responseLog.Error("received error response")
    } else {
        responseLog.Debug("received response")
    }
    return resp.StatusCode, bodyString, resp.Header, err
}

func (client *HammerspaceClient) generateRequest(verb, urlPath, body string) (*http.Request, error) {
    req, err := http.NewRequest(verb,
        fmt.Sprintf("%s%s%s", client.endpoint, BasePath, urlPath),
        bytes.NewBufferString(body))
    if err != nil {
        log.Error(err.Error())
        return nil, err
    }
    req.Header.Set("Accept", "application/json")
    req.Header.Set("Content-Type", "application/json")
    return req, err
}

func (client *HammerspaceClient) WaitForTaskCompletion(taskLocation string) (bool, error) {
    b := &backoff.Backoff{
        Max:    taskPollIntervalCap,
        Factor: 1.5,
        Jitter: true,
    }
    taskUrl, _ := url.Parse(taskLocation)
    taskId := path.Base(taskUrl.Path)
    startTime := time.Now()

    var task common.Task
    for time.Now().Sub(startTime) < taskPollTimeout {
        d := b.Duration()
        time.Sleep(d)

        log.Info(taskId)

        req, err := client.generateRequest("GET", "/tasks/"+taskId, "")
        if err != nil {
            log.Error("Failed to generate request object")
            os.Exit(1)
        }
        statusCode, respBody, _, err := client.doRequest(*req)
        if err != nil {
            return false, err
        }
        if statusCode != 200 {
            return false, errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
        }

        err = json.Unmarshal([]byte(respBody), &task)
        if err != nil {
            log.Error(err)
            return false, nil
        }
        if task.ExitValue != "NONE" {
            if task.ExitValue == "COMPLETED" {
                return true, nil
            } else {
                log.Error(fmt.Sprintf("Task %s, of type %s, failed. Exit value is %s", task.Uuid, task.Action, task.ExitValue))
                return false, nil
            }
        }
    }
    return false, errors.New(fmt.Sprintf("Task %s, of type %s, failed to complete within time limit. Current status is %s", task.Uuid, task.Action, task.Status))
}

func (client *HammerspaceClient) ListShares() ([]common.ShareResponse, error) {
    req, err := client.generateRequest("GET", "/shares", "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return nil, err
    }
    if statusCode != 200 {
        return nil, errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }

    var shares []common.ShareResponse
    err = json.Unmarshal([]byte(respBody), &shares)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
    }
    log.Debug(fmt.Sprintf("Found %d shares", len(shares)))

    return shares, nil
}


func (client *HammerspaceClient) ListObjectives() ([]common.ClusterObjectiveResponse, error) {
    req, err := client.generateRequest("GET", "/objectives", "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return nil, err
    }
    if statusCode != 200 {
        return nil, errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }

    var objs []common.ClusterObjectiveResponse
    err = json.Unmarshal([]byte(respBody), &objs)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
    }
    log.Debug(fmt.Sprintf("Found %d objectives", len(objs)))

    return objs, nil
}

func (client *HammerspaceClient) ListObjectiveNames() ([]string, error) {
    objectives, err := client.ListObjectives()
    if err != nil {
        return nil, err
    }

    objectiveNames := make([]string, len(objectives))
    for i, o := range objectives {
        objectiveNames[i] = o.Name
    }

    return objectiveNames, nil
}

func (client *HammerspaceClient) GetShare(name string) (*common.ShareResponse, error) {
    req, err := client.generateRequest("GET", "/shares/"+name, "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return nil, err
    }
    if statusCode == 404 {
        return nil, nil
    }
    if statusCode != 200 {
        return nil, errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }

    var share common.ShareResponse
    err = json.Unmarshal([]byte(respBody), &share)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
    }
    return &share, err
}

func (client *HammerspaceClient) GetShareRawFields(name string) (map[string]interface{}, error) {
    req, err := client.generateRequest("GET", "/shares/"+name, "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return nil, err
    }
    if statusCode == 404 {
        return nil, nil
    }
    if statusCode != 200 {
        return nil, errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }

    var share map[string]interface{}
    err = json.Unmarshal([]byte(respBody), &share)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
    }
    return share, err
}

func (client *HammerspaceClient) GetFile(path string) (*common.File, error) {
    req, err := client.generateRequest("GET", "/files?path="+path, "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return nil, err
    }

    // FIXME: we get a 500 from the api if it does not exist, should be a 404
    if statusCode == 500 {
        return nil, nil
    }
    if statusCode == 404 {
        return nil, nil
    }
    if statusCode != 200 {
        return nil, errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }
    var file common.File
    err = json.Unmarshal([]byte(respBody), &file)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
    }
    return &file, nil
}

func (client *HammerspaceClient) DoesFileExist(path string) (bool, error) {
    file, err := client.GetFile(path)
    return file != nil, err
}




func (client *HammerspaceClient) CreateShare(name string,
    exportPath string,
    size int64, //size in bytes
    objectives []string,
    exportOptions []common.ShareExportOptions,
    deleteDelay int64,
    comment string) error {

    log.Debug("Creating share: " + name)
    extendedInfo := common.GetCommonExtendedInfo()
    if deleteDelay >= 0 {
        extendedInfo["csi_delete_delay"] = strconv.Itoa(int(deleteDelay))
    }

    share := common.ShareRequest{
        Name:          name,
        ExportPath:    exportPath,
        ExportOptions: exportOptions,
        ExtendedInfo:  extendedInfo,
        Comment:       comment,
    }
    if size > 0 {
        share.Size = size
    }

    shareString := new(bytes.Buffer)
    json.NewEncoder(shareString).Encode(share)

    req, err := client.generateRequest("POST", "/shares", shareString.String())
    statusCode, _, respHeaders, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return err
    }
    if statusCode == 400 {
        // FIXME: We get a 400 if there is already a share-create task for a share with this name
        // can we check if a task exists somehow?
    }
    if statusCode != 202 {
        return errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 202))
    }

    // ensure the location header is set and also make sure length >= 1
    if locs, exists := respHeaders["Location"]; exists {
        success, err := client.WaitForTaskCompletion(locs[0])
        if err != nil {
            log.Error(err)
            return err
        }
        if !success {
            return errors.New("Share failed to create")
            defer client.DeleteShare(share.Name, 0)
        }

    } else {
        log.Errorf("No task returned to monitor")
    }

    // Set objectives on share
    err = client.SetObjectives(name, "/", objectives, true)
    if err != nil {
        log.Errorf("Failed to set objectives %s, %v", objectives, err)
        return err
    }

    return nil
}

func (client *HammerspaceClient) CreateShareFromSnapshot(name string,
    exportPath string,
    size int64, //size in bytes
    objectives []string,
    exportOptions []common.ShareExportOptions,
    deleteDelay int64,
    comment string,
    snapshotPath string) error {
    log.Debug("Creating share from snapshot: " + name)
    extendedInfo := common.GetCommonExtendedInfo()
    if deleteDelay >= 0 {
        extendedInfo["csi_delete_delay"] = strconv.Itoa(int(deleteDelay))
    }

    ////// FIXME: Replace with new api to clone a snapshot to a new share
    share := common.ShareRequest{
        Name:          name,
        ExportPath:    exportPath,
        ExportOptions: exportOptions,
        ExtendedInfo:  extendedInfo,
        Comment:       comment,
    }
    if size > 0 {
        share.Size = size
    }

    shareString := new(bytes.Buffer)
    json.NewEncoder(shareString).Encode(share)


    req, err := client.generateRequest("POST", "/shares", shareString.String())
    statusCode, _, respHeaders, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return err
    }
    if statusCode == 400 {
        // FIXME: We get a 400 if there is already a share-create task for a share with this name
        // can we check if a task exists somehow?
    }
    if statusCode != 202 {
        return errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 202))
    }

    // ensure the location header is set and also make sure length >= 1
    if locs, exists := respHeaders["Location"]; exists {
        success, err := client.WaitForTaskCompletion(locs[0])
        if err != nil {
            log.Error(err)
            return err
        }
        if !success {
            return errors.New("Share failed to create")
            defer client.DeleteShare(share.Name, 0)
        }

    } else {
        log.Errorf("No task returned to monitor")
    }

    ////// End FIXME

    // Set objectives on share
    err = client.SetObjectives(name, "/", objectives, true)
    if err != nil {
        log.Errorf("Failed to set objectives %s, %v", objectives, err)
        return err
    }

    return nil
}

// Set objectives on a share, at the specified path, optionally clearing previously-set objectives at the path
// The path must start with a slash
func (client *HammerspaceClient) SetObjectives(shareName string,
    path string,
    objectives []string,
    replaceExisting bool) error {
    log.Debugf("Setting objectives. Share=%s, Path=%s, Objectives=%v: ", shareName, path, objectives)
    // Set objectives on share at path
    cleared := false
    for _, objectiveName := range objectives {
        urlPath := fmt.Sprintf("/shares/%s/objective-set?path=%s&objective-identifier=%s",
            shareName, path, objectiveName)
        if replaceExisting && !cleared {
            urlPath += "&clear-existing=true"
            cleared = true
        }
        req, err := client.generateRequest("POST", urlPath, "")
        if err != nil {
            log.Errorf("Failed to set objective %s on share %s at path %s, %v",
                objectiveName, shareName, path, err)
            return err
        }
        statusCode, _, _, err := client.doRequest(*req)
        if err != nil {
            log.Errorf("Failed to set objective %s on share %s at path %s, %v",
                objectiveName, shareName, path, err)
            return err
        }
        if statusCode != 200 {
            //FIXME: err is not set here
            log.Errorf("Failed to set objective %s on share %s at path %s, %v",
                objectiveName, shareName, path, err)
            return errors.New(fmt.Sprint("failed to set objective"))
        }
    }

    return nil
}


func (client *HammerspaceClient) UpdateShareSize(name string,
    size int64, //size in bytes
    ) error {

    log.Debugf("Update share size : %s to %v", name, size)

    share, err := client.GetShareRawFields(name)
    if err != nil {
        return errors.New(common.ShareNotFound)
    }

    share["shareSizeLimit"] = size
    shareString := new(bytes.Buffer)
    json.NewEncoder(shareString).Encode(share)

    req, err := client.generateRequest("PUT", "/shares/" + name, shareString.String())
    statusCode, _, respHeaders, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return err
    }
    if statusCode == 400 {
        //
    }
    if statusCode != 202 {
        return errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 202))
    }

    // ensure the location header is set and also make sure length >= 1
    if locs, exists := respHeaders["Location"]; exists {
        success, err := client.WaitForTaskCompletion(locs[0])
        if err != nil {
            log.Error(err)
            return err
        }
        if !success {
            return errors.New("Share failed to update")
        }

    } else {
        log.Errorf("No task returned to monitor")
    }

    return nil
}

func (client *HammerspaceClient) DeleteShare(name string, deleteDelay int64) error {
    queryParams := "?delete-path=true"
    if deleteDelay >= 0 {
        queryParams = queryParams + "&delete-delay=" + strconv.Itoa(int(deleteDelay))
    }
    req, err := client.generateRequest("DELETE", "/shares/"+url.PathEscape(name)+queryParams, "")
    statusCode, body, respHeaders, err := client.doRequest(*req)
    if err != nil {
        return err
    }
    if statusCode == 400 {
        if strings.Contains(body, "Cannot remove a share with state REMOVED.") {
            return nil
        }
    }
    if statusCode == 404 || statusCode == 200 {
        return nil
    }
    if statusCode != 202 {
        return errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 202))
    }

    // ensure the location header is set and also make sure length >= 1
    if locs, exists := respHeaders["Location"]; exists {
        if !exists {
            log.Errorf("No task returned to monitor")
        } else {
            success, err := client.WaitForTaskCompletion(locs[0])
            if err != nil {
                log.Error(err)
            }
            if !success {
                return errors.New("share-delete task failed")
            }
        }
    }

    return nil
}

func (client *HammerspaceClient) SnapshotShare(shareName string) (string, error) {
    req, err := client.generateRequest("POST",
        fmt.Sprintf("/share-snapshots/snapshot-create/%s", url.PathEscape(shareName)), "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return "", err
    }
    if statusCode != 200 {
        return "", errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }

    //var snapshotNames []string
    //err = json.Unmarshal([]byte(respBody), &snapshotNames)
    //if err != nil {
    //	log.Error("Error parsing JSON response: " + err.Error())
    //	return "", err
    //}
    // FIXME: currently the API just returns the raw string for the snapshot name

    return respBody, nil
}

func (client *HammerspaceClient) GetShareSnapshots(shareName string) ([]string, error) {
    req, _ := client.generateRequest("GET",
        fmt.Sprintf("/share-snapshots/snapshot-list/%s", url.PathEscape(shareName)), "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        return nil, err
    }
    if statusCode != 200 {
        return []string{}, errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }

    var snapshotNames []string
    err = json.Unmarshal([]byte(respBody), &snapshotNames)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
        return []string{}, err
    }
// Need to prune the snapshot name current from the list
// Needs to work for a slice with only current as an entry as well as when there are many snapshots and current is somewhere in the list
   for i, v := range snapshotNames {
        if v == "current" {
            snapshotNames = append(snapshotNames[:i], snapshotNames[i+1:]...)
            break
        }
   }

    return snapshotNames, nil
}

func (client *HammerspaceClient) DeleteShareSnapshot(shareName, snapshotName string) error {
    req, _ := client.generateRequest("POST",
        fmt.Sprintf("/share-snapshots/snapshot-delete/%s/%s",
            url.PathEscape(shareName), url.PathEscape(snapshotName)), "")
    statusCode, _, _, err := client.doRequest(*req)

    if err != nil {
        return err
    }

    if statusCode == 404 || statusCode == 200 {
        return nil
    } else {
        return errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }
}

func (client *HammerspaceClient) GetFileSnapshots(filePath string) ([]common.FileSnapshot, error) {
    req, _ := client.generateRequest("GET",
        fmt.Sprintf("/file-snapshots/list?filename-expression=%s", url.PathEscape(filePath)), "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        return nil, err
    }
    if statusCode != 200 {
        return []common.FileSnapshot{}, errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }

    var snapshots []common.FileSnapshot
    err = json.Unmarshal([]byte(respBody), &snapshots)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
        return []common.FileSnapshot{}, err
    }

    return snapshots, nil
}

func (client *HammerspaceClient) DeleteFileSnapshot(filePath, snapshotName string) error {
    // Get only the timestamp from the snapshot path
    snapshotTime := strings.Join(strings.SplitN(url.PathEscape(path.Base(snapshotName)),
                                           "-", 6)[0:5],
                            "-")

    req, _ := client.generateRequest("POST",
        fmt.Sprintf("/file-snapshots/delete?filename-expression=%s&date-time-expression=%s", url.PathEscape(filePath), url.PathEscape(snapshotTime)), "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        return err
    }

    // FIXME: currently we get a 400 if the snapshot does not exist, instead of a 404
    if statusCode == 400 {
        if strings.Contains(respBody, "No snapshots found for path") {
            return nil
        }
    }

    if statusCode == 404 || statusCode == 200 {
        return nil
    } else {
        return errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }
}

func (client *HammerspaceClient) SnapshotFile(filepath string) (string, error) {
    req, err := client.generateRequest("POST", fmt.Sprintf("/file-snapshots/create?filename-expression=%s", url.PathEscape(filepath)), "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return "", err
    }
    if statusCode != 200 {
        return "", errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }
    var snapshotNames []string
    err = json.Unmarshal([]byte(respBody), &snapshotNames)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
        return "", err
    }

    return snapshotNames[0], nil
}

func (client *HammerspaceClient) RestoreFileSnapToDestination(snapshotPath, filePath string) error {
    req, err := client.generateRequest("POST", fmt.Sprintf("/file-snapshots/%s/%s", url.PathEscape(snapshotPath), url.PathEscape(filePath)), "")
    statusCode, _, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return err
    }
    if statusCode != 200 {
        return errors.New(fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }
    return nil
}

func (client *HammerspaceClient) GetClusterAvailableCapacity() (int64, error) {
    req, err := client.generateRequest("GET", "/cntl/state", "")
    statusCode, respBody, _, err := client.doRequest(*req)

    if err != nil {
        log.Error(err)
        return 0, err
    }
    if statusCode != 200 {
        return 0, errors.New(
            fmt.Sprintf(common.UnexpectedHSStatusCode, statusCode, 200))
    }

    var cluster common.ClusterResponse
    err = json.Unmarshal([]byte(respBody), &cluster)
    if err != nil {
        log.Error("Error parsing JSON response: " + err.Error())
    }
    free, err := strconv.ParseInt(cluster.Capacity["free"], 10, 64)
    if err != nil {
        log.Error("Error parsing free cluster capacity: " + err.Error())
    }

    return free, nil
}
