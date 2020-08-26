# CTO Tenancy Identity Synchronizer
This is a purpose-built app that takes user information from a bespoke user service and leverages Oracle Identity Cloud (IDCS), Oracle Integration Cloud (OIC), and Oracle Content & Experience (OCE) APIs to populate appropriate user/group data all repositories.

The app requires a file named *config.json* to be present the same directory from which the app is run.  A sample file (with identifying credentials removed) looks like this:

```json
{
    "IdcsBaseURL": "https://idcs-{{your_stripe}}.identity.oraclecloud.com",
    "IdcsClientID": "{{your_client_id}}",
    "IdcsClientSecret": "{{your_client_secret}}",
    "IdcsCreateNewUserPayload": "{\"schemas\":[\"urn:ietf:params:scim:schemas:core:2.0:User\"],\"name\":{\"givenName\":\"%FIRSTNAME%\",\"familyName\":\"%LASTNAME%\"},\"active\":true,\"userName\":\"%USERNAME%\",\"emails\":[{\"value\":\"%USERNAME%\",\"type\":\"work\",\"primary\":true},{\"value\":\"%USERNAME%\",\"primary\":false,\"type\":\"recovery\", \"urn:ietf:params:scim:schemas:oracle:idcs:extension:user:User:isFederatedUser\": true}]}",
    "IdcsAddUserToGroupPayload": "{\"schemas\":[\"urn:ietf:params:scim:api:messages:2.0:PatchOp\"],\"Operations\":[{\"op\":\"add\",\"path\":\"members\",\"value\":[{\"value\":\"%USERID%\",\"type\":\"User\"}]}]}",
    "AriaServiceEndpointURL": "{{aria_service_endpoint}}",
    "AriaServiceUsername": "{{aria_service_username}}",
    "AriaServicePassword": "{{aria_service_password}}",
    "ManagerGroupNames": "Prod_ECAL_Managers,Prod_ECAL_Artifact_Downloaders,Prod_Analytics_ServiceViewers,Prod_STS_Managers",
    "UserGroupNames": "Prod_ECAL_Users,Prod_ECAL_Artifact_Downloaders,Prod_STS_Users",
    "VbcsUsername": "{{serviceaccount_username}}",
    "VbcsPassword": "{{serviceaccount_password}}",
    "EcalUserEndpoint": "https://{{your_instance_name}}.integration.ocp.oraclecloud.com/ic/builder/design/ECAL/live/resources/data/User1",
    "EcalUserAddPayload": "{\"userEmail\":\"%USERNAME%\",\"firstName\":\"%FIRSTNAME%\",\"lastName\":\"%LASTNAME%\",\"manager\":\"%MANAGER%\",\"roleName\":%ROLE%,\"businessSegment\":\"%LOB%\"}",
    "EcalUpdateManagerPayload": "{\"userEmail\":\"%USERNAME%\",\"firstName\":\"%FIRSTNAME%\",\"lastName\":\"%LASTNAME%\",\"manager\":\"%MANAGER%\",\"roleName\":%ROLE%,\"businessSegment\":\"%LOB%\"}",
    "EcalUserRoleCode": "{{generated_id_of_user_role_in_ecal_roletype_business_object}}",
    "EcalManagerRoleCode": "{{primary_id_of_manager_role_in_ecal_roletype_business_object}}",
    "StsUserEndpoint": "https://{{your_instance_name}}.integration.ocp.oraclecloud.com/ic/builder/rt/STS/live/resources/data/STSUser",
    "StsUserAddPayload": "{\"userEmail\":\"%USERNAME%\",\"firstName\":\"%FIRSTNAME%\",\"lastName\":\"%LASTNAME%\",\"manager\":\"%MANAGER%\",\"businessSegment\":\"%LOB%\",\"roleName\":%ROLE%,\"path\":1}",
    "StsUpdateManagerPayload": "{\"userEmail\":\"%USERNAME%\",\"firstName\":\"%FIRSTNAME%\",\"lastName\":\"%LASTNAME%\",\"manager\":\"%MANAGER%\",\"roleName\":%ROLE%,\"businessSegment\":\"%LOB%\"}",
    "StsUserRoleCode": "{{generated_id_of_user_role_in_sts_role_business_object}}",
    "StsManagerRoleCode": "{{generated_id_of_manager_role_in_sts_role_business_object}}",
    "OceBaseURL": "https://{{your_instance_name}}.cec.ocp.oraclecloud.com",
    "OceUsername": "{{serviceaccount_username}}",
    "OcePassword": "{{serviceaccount_password}}",
    "OceArtifactsFolderID": "{{OCE id of root artifacts folder, get it from looking at URL in OCE web view}}",
    "OceAddUserPayload": "{\"userID\":\"%USERNAME%\",\"role\":\"downloader\"}"
}
```
When used with the OCI Secrets Service the format of any vaulted credentials must be in the form of:  
```
[vault]FieldName:SecretOCID
```

For example:
``` 
"IdcsClientSecret": "[vault]IdcsClientSecret:ocid1.vaultsecret.oc1.iad.amaaaaaabxdvnfaaojh62dolelcp4xk93xrms6jfagdec2p3slzs7fx2iicq"
```

This utility supports both synchronizing from the source to IDCS/VBCS as well as removing all users (based on the same source) from the target systems.

## Usage
```
cto-identity-sync [--help || --add || --delete]

--help:     Prints this message
--add:      Synchronizes users from Aria service to IDCS/VBCS/OCE apps
--delete:   Removes users returned from Aria service from IDCS/VBCS/OCE apps
--clean:    Removes users from IDCS/VBCS/OCE who are no longer found in the Aria service
```

## Building the service from code
The following steps can be followed to build this service on Oracle Cloud Infrastructure (OCI):
1. Create a VCN with all related resources and update default security list to allow ingress access for TCP/80 and TCP/443
1. Create compute instance from "Oracle Developer" marketplace image
1. SSH into instance and open ingress for TCP/80 in linux firewall
    1. sudo firewall-cmd --zone=public --add-port=80/tcp --permanent
    1. sudo firewall-cmd --reload
1. Clone git repo (git clone {{this repo name}})
    1. git clone https://github.com/eshneken/cto-identity-sync
1. Download gjson dependency package 
    1. go get -u github.com/tidwall/gjson
1. Download OCI golang SDK and make sure this instance either has ~/.oci/config set (local mode) or is configured for InstancePrincipal authentication 
    1. go get -u github.com/oracle/oci-go-sdk
1. Add a config.json file to the cto-identity-sync directory with the appropriate values
1. Build the package
    1. sudo go build
1. Increase the number of file descriptors
    1. sudo vi /etc/security/limits.conf
    1. Add the following lines to the end
        1. * soft nofile 65536
        1. * hard nofile 65536
1. Open the opc user's crontab
    1. crontab -e
1. Add a cron job to run the identity sync tool once a day at 4am
    1. 0 4 * * * cd /home/opc/cto-identity-sync/;./cto-identity-sync --add &> /home/opc/identity.out

## Related Services
* CTO Bizlogic Helper Service:  https://github.com/eshneken/cto-bizlogic-helper

## Principles for API Usage
* Setting up IDCS with client application to retrieve JWT bearer tokens:  https://www.oracle.com/webfolder/technetwork/tutorials/obe/cloud/idcs/idcs_rest_1stcall_obe/rest_1stcall.html
 * Using IDCS APIs:
https://www.oracle.com/webfolder/technetwork/tutorials/obe/cloud/idcs/idcs_rest_users_obe/rest_users.html
* OIC REST API Reference:  https://docs.oracle.com/en/cloud/paas/identity-cloud/rest-api/
* Working with VBCS Business Object APIs:  https://docs.oracle.com/en/cloud/paas/app-builder-cloud/consume-rest/index.html

## Third Party Packages Used

 * Read-only JSON pathing support:  https://github.com/tidwall/gjson
 * OCI Golang SDK:  https://github.com/oracle/oci-go-sdk
 
