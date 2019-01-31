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

package client

const (
	FakeShareRoot = `
{
    "uoid": {
        "uuid": "acd90e88-ed23-3464-90ee-320e11de31ae",
        "objectType": "SHARE"
    },
    "created": "1548944448931",
    "modified": "1548944448931",
    "extendedInfo": {},
    "comment": null,
    "name": "root",
    "path": "/",
    "internalId": 1,
    "shareState": "PUBLISHED",
    "exportOptions": [
        {
            "id": "1",
            "subnet": "*",
            "accessPermissions": "RW",
            "rootSquash": false
        }
    ],
    "shareSnapshots": [],
    "shareSizeLimit": null,
    "warnUtilizationPercentThreshold": null,
    "totalNumberOfFiles": "5",
    "numberOfOpenFiles": "0",
    "space": {
        "total": "64393052160",
        "used": "0",
        "available": "63909851136",
        "percent": 0
    },
    "scheduledPurgeTime": null
}
`
	FakeShare1 = `
{
	"uoid": {
		"uuid": "ac486652-6957-43cd-ac75-9885b3b3e9c9",
		"objectType": "SHARE"
	},
	"created": "1549325841555",
	"modified": "1549325864146",
	"extendedInfo": {
		"csi_created_by_plugin_version": "test_version",
		"csi_created_by_plugin_name": "test_plugin",
		"csi_delayed_delete": "0"
	},
	"comment": null,
	"name": "test-client-code",
	"path": "/test-client-code",
	"internalId": 13,
	"shareState": "PUBLISHED",
	"exportOptions": [
		{
			"id": "11",
			"subnet": "*",
			"accessPermissions": "RW",
			"rootSquash": false
		}
	],
	"shareSnapshots": [],
	"shareSizeLimit": "1073741824",
	"warnUtilizationPercentThreshold": 90,
	"utilizationState": "NORMAL",
	"preferredDomain": null,
	"unmappedUser": null,
	"unmappedGroup": null,
	"participantId": 0,
	"stats": [],
	"totalNumberOfFiles": "1",
	"numberOfOpenFiles": "0",
	"space": {
		"total": "1073741824",
		"used": "0",
		"available": "1073741824",
		"percent": 0
	},
	"scheduledPurgeTime": null
}
`

	FakeTaskCompleted = `
{
    "uuid": "a59ad344-6f1a-4ef2-b1e2-1d232707978d",
    "name": "share-create",
    "status": "COMPLETED",
    "exitValue": "COMPLETED"
}
`

	FakeTaskFailed = `
{
    "uuid": "b59ad344-6f1a-4ef2-b1e2-1d232707978d",
    "name": "share-create",
    "status": "FAILED",
    "exitValue": "Status: 500, Output: random"
}
`

	FakeTaskRunning = `
{
    "uuid": "c59ad344-6f1a-4ef2-b1e2-1d232707978d",
    "name": "share-create",
    "status": "VALIDATING",
    "exitValue": "NONE"
}
`
)
