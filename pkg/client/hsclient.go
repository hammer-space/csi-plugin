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
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/publicsuffix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hammer-space/csi-plugin/pkg/common"
	"github.com/jpillora/backoff"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	BasePath            = "/mgmt/v1.2/rest"
	taskPollTimeout     = 3600 * time.Second // Seconds
	taskPollIntervalCap = 30 * time.Second   //Seconds, The maximum duration between calls when polling task objects
)

var (
	fipIndices sync.Map // map[string]*uint32
	tracer     = otel.Tracer("hammerspace-csi",
		trace.WithInstrumentationAttributes(
			attribute.String("service.name", "hammerspace-csi"),
			attribute.String("version", common.Version),
		),
	)
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
func (client *HammerspaceClient) GetPortalFloatingIp(ctx context.Context) (string, error) {
	// Instead of using /cntl, use /cntl/state to simplify processing of the JSON
	// struct. If using /cntl, add [] before cluster struct
	req, err := client.generateRequest(ctx, "GET", "/cntl/state", "")
	if err != nil {
		return "", err
	}
	statusCode, respBody, _, err := client.doRequest(*req)
	if err != nil {
		return "", err
	}
	if statusCode != 200 {
		return "", fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}
	var clusters common.Cluster
	err = json.Unmarshal([]byte(respBody), &clusters)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
		return "", err
	}

	addresses := make([]string, 0, len(clusters.PortalFloatingIps))
	for _, p := range clusters.PortalFloatingIps {
		addresses = append(addresses, p.Address)
	}
	// If no floating IPs are found, return an error
	if len(addresses) == 0 {
		return "", fmt.Errorf("no floating IPs found")
	}

	// Use stable cluster identifier (e.g., name)
	clusterKey := clusters.Name
	if clusterKey == "" {
		clusterKey = uuid.NewString() // fallback if name is empty
	}

	// Load or initialize atomic index for this cluster
	val, _ := fipIndices.LoadOrStore(clusterKey, new(uint32))
	index := val.(*uint32)

	// Get round-robin ordered list based on atomic index
	ordered := GetRoundRobinOrderedList(index, addresses)

	// Strict sequential check — pick first valid FIP in round-robin order
	for _, fip := range ordered {
		ok, err := common.CheckNFSExports(fip)
		if err != nil {
			log.Warnf("Failed checking exports on FIP %s: %v", fip, err)
			continue
		}
		if ok {
			log.Infof("Selected FIP via strict round-robin: %s", fip)
			return fip, nil
		}
	}
	log.Warnf("No valid floating IPs found in round-robin order: %v", ordered)
	return "", fmt.Errorf("no valid floating IPs found")
}

// GetDataPortals returns a list of operational data-portals
// those with a matching nodeID are put at the top of the list
func (client *HammerspaceClient) GetDataPortals(ctx context.Context, nodeID string) ([]common.DataPortal, error) {
	req, err := client.generateRequest(ctx, "GET", "/data-portals/", "")

	if err != nil {
		log.Error(err)
		return nil, err
	}

	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return nil, err
	}
	if statusCode != 200 {
		return nil, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
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
	v := url.Values{}
	v.Add("username", client.username)
	v.Add("password", client.password)

	resp, err := client.httpclient.PostForm(fmt.Sprintf("%s%s/login", client.endpoint, BasePath), v)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
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
	body, err := io.ReadAll(resp.Body)
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

// generateRequest creates a new HTTP request with the given verb, URL path, and body.
func (client *HammerspaceClient) generateRequest(ctx context.Context, verb, urlPath, body string) (*http.Request, error) {
	ctx, span := tracer.Start(ctx, "HammerspaceClient.generateRequest")
	defer span.End()

	fullURL := fmt.Sprintf("%s%s%s", client.endpoint, BasePath, urlPath)
	req, err := http.NewRequestWithContext(ctx, verb, fullURL, bytes.NewBufferString(body))
	if err != nil {
		log.Error(err.Error())
		span.RecordError(err)
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Agent", "Hammerspace CSI Plugin/"+common.Version)

	// Inject trace context into headers
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	// Add span metadata
	span.SetAttributes(
		attribute.String("http.method", verb),
		attribute.String("http.url", fullURL),
		attribute.String("component", "hammerspace-csi"),
	)

	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		log.Warn("No active span context found in ctx")
	} else {
		log.Infof("trace: method=%s url=%s trace_id=%s span_id=%s",
			req.Method,
			req.URL.String(),
			spanCtx.TraceID().String(),
			spanCtx.SpanID().String(),
		)
	}

	return req, nil
}

func (client *HammerspaceClient) WaitForTaskCompletion(ctx context.Context, taskLocation string) (bool, error) {
	b := &backoff.Backoff{
		Max:    taskPollIntervalCap,
		Factor: 1.5,
		Jitter: true,
	}
	taskUrl, _ := url.Parse(taskLocation)
	taskId := path.Base(taskUrl.Path)
	startTime := time.Now()

	var task common.Task
	for time.Since(startTime) < taskPollTimeout {
		d := b.Duration()
		time.Sleep(d)

		req, err := client.generateRequest(ctx, "GET", "/tasks/"+taskId, "")
		if err != nil {
			log.Error("Failed to generate request object")
			os.Exit(1)
		}
		statusCode, respBody, _, err := client.doRequest(*req)
		if err != nil {
			return false, err
		}
		if statusCode != 200 {
			return false, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
		}

		err = json.Unmarshal([]byte(respBody), &task)
		if err != nil {
			log.Error(err)
			return false, nil
		}
		if task.Status != "NONE" && task.Status != "EXECUTING" {
			if task.Status == "COMPLETED" || task.Status == "FAILED" || task.Status == "HALTED" || task.Status == "CANCELLED" {
				return true, nil
			} else {
				log.Error(fmt.Sprintf("Task %s, of type %s, failed. Exit value is %s", task.Uuid, task.Action, task.StatusMessage))
				return false, nil
			}
		}
	}
	return false, fmt.Errorf("task %s, of type %s, failed to complete within time limit. Current status is %s", task.Uuid, task.Action, task.Status)
}

func (client *HammerspaceClient) ListShares(ctx context.Context) ([]common.ShareResponse, error) {
	req, err := client.generateRequest(ctx, "GET", "/shares", "")
	if err != nil {
		log.Error(err)
		return nil, err
	}
	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return nil, err
	}
	if statusCode != 200 {
		return nil, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}

	var shares []common.ShareResponse
	err = json.Unmarshal([]byte(respBody), &shares)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
	}
	log.Debug(fmt.Sprintf("Found %d shares", len(shares)))

	return shares, nil
}

func (client *HammerspaceClient) ListObjectives(ctx context.Context) ([]common.ClusterObjectiveResponse, error) {
	req, err := client.generateRequest(ctx, "GET", "/objectives", "")
	if err != nil {
		log.Error(err)
		return nil, err
	}

	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return nil, err
	}
	if statusCode != 200 {
		return nil, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}

	var objs []common.ClusterObjectiveResponse
	err = json.Unmarshal([]byte(respBody), &objs)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
	}
	log.Debug(fmt.Sprintf("Found %d objectives", len(objs)))
	// set free capacity to cache expire in 5 min
	SetCacheData("OBJECTIVE_LIST", objs, 60*5)
	return objs, nil
}

func (client *HammerspaceClient) ListObjectiveNames(ctx context.Context) ([]string, error) {
	objectives, err := client.ListObjectives(ctx)
	if err != nil {
		return nil, err
	}

	objectiveNames := make([]string, len(objectives))
	for i, o := range objectives {
		objectiveNames[i] = o.Name
	}
	// set free capacity to cache expire in 5 min
	SetCacheData("OBJECTIVE_LIST_NAMES", objectiveNames, 60*5)
	return objectiveNames, nil
}

func (client *HammerspaceClient) ListVolumes(ctx context.Context) ([]common.VolumeResponse, error) {
	req, err := client.generateRequest(ctx, "GET", "/base-storage-volumes", "")
	if err != nil {
		log.Error(err)
		return nil, err
	}

	statusCode, respBody, _, err := client.doRequest(*req)
	if err != nil {
		log.Error(err)
		return nil, err
	}
	if statusCode != 200 {
		return nil, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}

	var volumes []common.VolumeResponse
	err = json.Unmarshal([]byte(respBody), &volumes)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
	}
	log.Debug(fmt.Sprintf("Found %d volumes", len(volumes)))

	return volumes, nil
}

func (client *HammerspaceClient) ListSnapshots(ctx context.Context, snapshot_id, volume_id string) ([]common.SnapshotResponse, error) {
	// Get all shares
	shares, err := client.ListShares(ctx)
	if err != nil || shares == nil {
		log.Error(err)
		return nil, err
	}

	var shareSnapshots []common.SnapshotResponse

	// Iterate over each share
	for _, share := range shares {
		// Skip shares that don't match the provided volume_id (if specified)
		if volume_id != "" && share.Name != volume_id {
			continue
		}

		// Get the snapshots from the /.snapshot/ directory of the share
		shareSnapshotDir := share.ExportPath + "/.snapshot/"
		shareFile, err := client.GetFile(ctx, shareSnapshotDir)
		if err != nil {
			log.Errorf("Failed to get share snapshots from %s: %v", shareSnapshotDir, err)
			return nil, err
		}

		// Iterate over the snapshots in the /.snapshot/ directory
		for _, snapshotFile := range shareFile.Children {
			snapshot := common.SnapshotResponse{
				Id:             snapshotFile.Name,
				Created:        snapshotFile.CreateTime,
				SourceVolumeId: share.Name,
				ReadyToUse:     true, // Assume true if the snapshot exists
				Size:           snapshotFile.Size,
			}

			// Filter by snapshot_id if provided
			if snapshot_id != "" && snapshot.Id != snapshot_id {
				continue
			}

			// Add the snapshot to the list
			shareSnapshots = append(shareSnapshots, snapshot)
		}
	}
	log.Infof("%v, %s, %s", shareSnapshots, snapshot_id, volume_id)
	return shareSnapshots, nil
}

func (client *HammerspaceClient) GetShare(ctx context.Context, name string) (*common.ShareResponse, error) {
	req, err := client.generateRequest(ctx, "GET", "/shares/"+url.PathEscape(name), "")
	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return nil, err
	}
	if statusCode == 404 {
		return nil, nil
	}
	if statusCode != 200 {
		return nil, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}

	var share common.ShareResponse
	err = json.Unmarshal([]byte(respBody), &share)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
	}
	return &share, err
}

func (client *HammerspaceClient) GetShareRawFields(ctx context.Context, name string) (map[string]interface{}, error) {
	req, err := client.generateRequest(ctx, "GET", "/shares/"+url.PathEscape(name), "")
	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return nil, err
	}
	if statusCode == 404 {
		return nil, nil
	}
	if statusCode != 200 {
		return nil, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}

	var share map[string]interface{}
	err = json.Unmarshal([]byte(respBody), &share)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
	}
	return share, err
}

func (client *HammerspaceClient) GetFile(ctx context.Context, path string) (*common.File, error) {
	req, err := client.generateRequest(ctx, "GET", "/files?path="+url.PathEscape(path), "")
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
		return nil, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}
	var file common.File
	err = json.Unmarshal([]byte(respBody), &file)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
	}
	return &file, nil
}

func (client *HammerspaceClient) DoesFileExist(ctx context.Context, path string) (bool, error) {
	file, err := client.GetFile(ctx, path)
	return file != nil, err
}

func (client *HammerspaceClient) CreateShare(ctx context.Context,
	name string,
	exportPath string,
	size int64, //size in bytes
	objectives []string,
	exportOptions []common.ShareExportOptions,
	deleteDelay int64,
	comment string) error {

	log.Debug("Creating share: " + name)
	extendedInfo := common.GetCommonExtendedInfo()
	if exportOptions == nil { // send empty list to api req
		exportOptions = make([]common.ShareExportOptions, 0)
	}
	if deleteDelay >= 0 {
		extendedInfo["csi_delete_delay"] = strconv.Itoa(int(deleteDelay))
	}
	if len(name) > 80 {
		return status.Error(codes.InvalidArgument, common.InvalidShareNameSize)
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

	req, err := client.generateRequest(ctx, "POST", "/shares", shareString.String())
	statusCode, _, respHeaders, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return err
	}
	if statusCode != 202 {
		if statusCode == 400 {
			shareTaskRunning, err := client.CheckIfShareCreateTaskIsRunning(ctx, name)
			log.Debug(fmt.Sprintf("Found share creating task running as: %v ", shareTaskRunning))
			if shareTaskRunning {
				return nil
			}
			return err
		}
		return fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 202)
	}

	// ensure the location header is set and also make sure length >= 1
	if locs, exists := respHeaders["Location"]; exists {
		success, err := client.WaitForTaskCompletion(ctx, locs[0])
		if err != nil {
			log.Error(err)
			return err
		}
		if !success {
			defer client.DeleteShare(ctx, share.Name, 0)
			return errors.New("Share failed to create")
		}

	} else {
		log.Errorf("No task returned to monitor")
	}

	// Set objectives on share
	err = client.SetObjectives(ctx, name, "/", objectives, true)
	if err != nil {
		log.Errorf("Failed to set objectives %s, %v", objectives, err)
		return err
	}

	return nil
}

func (client *HammerspaceClient) CreateShareFromSnapshot(ctx context.Context, name string,
	exportPath string,
	size int64, //size in bytes
	objectives []string,
	exportOptions []common.ShareExportOptions,
	deleteDelay int64,
	comment string,
	snapshotPath string) error {
	log.Debug("Creating share from snapshot: " + name)
	extendedInfo := common.GetCommonExtendedInfo()

	if exportOptions == nil { // send empty list to api req
		exportOptions = make([]common.ShareExportOptions, 0)
	}
	if deleteDelay >= 0 {
		extendedInfo["csi_delete_delay"] = strconv.Itoa(int(deleteDelay))
	}
	if len(name) > 80 {
		return status.Error(codes.InvalidArgument, common.InvalidShareNameSize)
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

	req, err := client.generateRequest(ctx, "POST", "/shares", shareString.String())
	statusCode, _, respHeaders, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return err
	}
	if statusCode != 202 {
		if statusCode == 400 {
			shareTaskRunning, err := client.CheckIfShareCreateTaskIsRunning(ctx, name)
			log.Debug(fmt.Sprintf("Found share creating task running as: %v ", shareTaskRunning))
			if shareTaskRunning {
				return nil
			}
			return err
		}
		return fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 202)
	}

	// ensure the location header is set and also make sure length >= 1
	if locs, exists := respHeaders["Location"]; exists {
		success, err := client.WaitForTaskCompletion(ctx, locs[0])
		if err != nil {
			log.Error(err)
			return err
		}
		if !success {
			defer client.DeleteShare(ctx, share.Name, 0)
			return errors.New("Share failed to create")
		}

	} else {
		log.Errorf("No task returned to monitor")
	}

	// Set objectives on share
	err = client.SetObjectives(ctx, name, "/", objectives, true)
	if err != nil {
		log.Errorf("Failed to set objectives %s, %v", objectives, err)
		return err
	}

	return nil
}

func (client *HammerspaceClient) CheckIfShareCreateTaskIsRunning(ctx context.Context, shareName string) (bool, error) {
	req, err := client.generateRequest(ctx, "GET", "/tasks", "")
	if err != nil {
		log.Error("Failed to generate request object")
		return false, err
	}
	statusCode, respBody, _, err := client.doRequest(*req)
	if err != nil {
		return false, err
	}
	if statusCode != 200 {
		return false, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}
	var tasks []common.Task
	err = json.Unmarshal([]byte(respBody), &tasks)
	if err != nil {
		log.Error(err)
		return false, nil
	}
	for _, task := range tasks {
		// log.Debug(fmt.Printf("Task Name: %v\n  Task Status: %s\n Share Name: %s\n", task.ParamsMap.Name, task.Status, shareName))
		if task.Status == "EXECUTING" && task.ParamsMap.Name == shareName {
			return true, nil
		}
	}
	return false, nil
}

// Set objectives on a share, at the specified path, optionally clearing previously-set objectives at the path
// The path must start with a slash
func (client *HammerspaceClient) SetObjectives(ctx context.Context, shareName string,
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
		req, err := client.generateRequest(ctx, "POST", urlPath, "")
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
			return errors.New("failed to set objective")
		}
	}

	return nil
}

// size in bytes
func (client *HammerspaceClient) UpdateShareSize(ctx context.Context, name string, size int64) error {

	log.Debugf("Update share size : %s to %v", name, size)

	share, err := client.GetShareRawFields(ctx, name)
	if err != nil {
		return errors.New(common.ShareNotFound)
	}

	share["shareSizeLimit"] = size
	shareString := new(bytes.Buffer)
	json.NewEncoder(shareString).Encode(share)

	req, err := client.generateRequest(ctx, "PUT", "/shares/"+name, shareString.String())
	if err != nil {
		log.Error(err)
		return err
	}
	statusCode, _, respHeaders, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return err
	}
	if statusCode == 400 {
		//
	}
	if statusCode != 202 {
		return fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 202)
	}

	// ensure the location header is set and also make sure length >= 1
	if locs, exists := respHeaders["Location"]; exists {
		success, err := client.WaitForTaskCompletion(ctx, locs[0])
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

func (client *HammerspaceClient) DeleteShare(ctx context.Context, name string, deleteDelay int64) error {
	queryParams := "?delete-path=true"
	if deleteDelay >= 0 {
		queryParams = queryParams + "&delete-delay=" + strconv.Itoa(int(deleteDelay))
	}
	req, err := client.generateRequest(ctx, "DELETE", "/shares/"+url.PathEscape(name)+queryParams, "")
	if err != nil {
		return err
	}
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
		return fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 202)
	}

	// ensure the location header is set and also make sure length >= 1
	if locs, exists := respHeaders["Location"]; exists {
		if !exists {
			log.Errorf("No task returned to monitor")
		} else {
			success, err := client.WaitForTaskCompletion(ctx, locs[0])
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

func (client *HammerspaceClient) SnapshotShare(ctx context.Context, shareName string) (string, error) {
	req, err := client.generateRequest(ctx, "POST",
		fmt.Sprintf("/share-snapshots/snapshot-create/%s", url.PathEscape(shareName)), "")
	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return "", err
	}
	if statusCode != 200 {
		return "", fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
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

func (client *HammerspaceClient) GetShareSnapshots(ctx context.Context, shareName string) ([]string, error) {
	req, _ := client.generateRequest(ctx, "GET",
		fmt.Sprintf("/share-snapshots/snapshot-list/%s", url.PathEscape(shareName)), "")
	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		return nil, err
	}
	if statusCode != 200 {
		return []string{}, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
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

func (client *HammerspaceClient) DeleteShareSnapshot(ctx context.Context, shareName, snapshotName string) error {
	req, _ := client.generateRequest(ctx, "POST",
		fmt.Sprintf("/share-snapshots/snapshot-delete/%s/%s",
			url.PathEscape(shareName), url.PathEscape(snapshotName)), "")
	statusCode, _, _, err := client.doRequest(*req)

	if err != nil {
		return err
	}

	if statusCode == 404 || statusCode == 200 {
		return nil
	} else {
		return fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}
}

func (client *HammerspaceClient) GetFileSnapshots(ctx context.Context, filePath string) ([]common.FileSnapshot, error) {
	req, _ := client.generateRequest(ctx, "GET",
		fmt.Sprintf("/file-snapshots/list?filename-expression=%s", url.PathEscape(filePath)), "")
	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		return nil, err
	}
	if statusCode != 200 {
		return []common.FileSnapshot{}, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}

	var snapshots []common.FileSnapshot
	err = json.Unmarshal([]byte(respBody), &snapshots)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
		return []common.FileSnapshot{}, err
	}

	return snapshots, nil
}

func (client *HammerspaceClient) DeleteFileSnapshot(ctx context.Context, filePath, snapshotName string) error {
	// Get only the timestamp from the snapshot path
	snapshotTime := strings.Join(strings.SplitN(url.PathEscape(path.Base(snapshotName)),
		"-", 6)[0:5],
		"-")

	req, _ := client.generateRequest(ctx, "POST",
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
		return fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}
}

func (client *HammerspaceClient) SnapshotFile(ctx context.Context, filepath string) (string, error) {
	req, err := client.generateRequest(ctx, "POST", fmt.Sprintf("/file-snapshots/create?filename-expression=%s", url.PathEscape(filepath)), "")
	if err != nil {
		log.Error(err)
		return "", err
	}

	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return "", err
	}
	if statusCode != 200 {
		return "", fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}
	var snapshotNames []string
	err = json.Unmarshal([]byte(respBody), &snapshotNames)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
		return "", err
	}

	return snapshotNames[0], nil
}

func (client *HammerspaceClient) RestoreFileSnapToDestination(ctx context.Context, snapshotPath, filePath string) error {
	req, err := client.generateRequest(ctx, "POST", fmt.Sprintf("/file-snapshots/%s/%s", url.PathEscape(snapshotPath), url.PathEscape(filePath)), "")

	if err != nil {
		log.Error(err)
		return err
	}

	statusCode, _, _, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return err
	}
	if statusCode != 200 {
		return fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}
	return nil
}

func (client *HammerspaceClient) GetClusterAvailableCapacity(ctx context.Context) (int64, error) {
	req, err := client.generateRequest(ctx, "GET", "/cntl/state", "")
	if err != nil {
		log.Error(err)
		return 0, err
	}

	statusCode, respBody, _, err := client.doRequest(*req)

	if err != nil {
		log.Error(err)
		return 0, err
	}
	if statusCode != 200 {
		return 0, fmt.Errorf(common.UnexpectedHSStatusCode, statusCode, 200)
	}

	var cluster common.ClusterResponse
	err = json.Unmarshal([]byte(respBody), &cluster)
	if err != nil {
		log.Error("Error parsing JSON response: " + err.Error())
	}
	// set free capacity to cache expire in 5 min
	SetCacheData("FREE_CAPACITY", cluster.Capacity["free"], 60*5)

	free := cluster.Capacity["free"]
	if err != nil {
		log.Error("Error parsing free cluster capacity: " + err.Error())
	}

	return free, nil
}
