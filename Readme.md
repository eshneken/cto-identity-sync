# CTO Tenancy Identity Synchronizer
This is a purpose-built app that takes user information from a bespoke user service and leverages Oracle Identity Cloud (IDCS) and Oracle Integration Cloud (OIC) APIs to populate appropriate user/group data in both repositories.

The app requires a file named *config.json* to be present the same directory from which the app is run.  A sample file (with identifying credentials removed) looks like this:

```json
{
    "IdcsBaseURL": "https://idcs-{{your_stripe}}.identity.oraclecloud.com",
    "IdcsClientID": "{{your_client_id}}",
    "IdcsClientSecret": "{{your_client_secret}}",
    "IdcsCreateNewUserPayload": "{\"schemas\":[\"urn:ietf:params:scim:schemas:core:2.0:User\"],\"name\":{\"givenName\":\"%FIRSTNAME%\",\"familyName\":\"%LASTNAME%\"},\"active\":true,\"userName\":\"%USERNAME%\",\"emails\":[{\"value\":\"%USERNAME%\",\"type\":\"work\",\"primary\":true},{\"value\":\"%USERNAME%\",\"primary\":false,\"type\":\"recovery\", \"urn:ietf:params:scim:schemas:oracle:idcs:extension:user:User:isFederatedUser\": true}]}",
    "IdcsAddUserToGroupPayload": "{\"schemas\":[\"urn:ietf:params:scim:api:messages:2.0:PatchOp\"],\"Operations\":[{\"op\":\"add\",\"path\":\"members\",\"value\":[{\"value\":\"%USERID%\",\"type\":\"User\"}]}]}",
    "AriaServiceEndpointURL": "{{aria_service_endpoint}}",
    "ManagerGroupNames": "STS_Managers,ECAL_Managers",
    "UserGroupNames": "STS_Users,ECAL_Users",
    "VbcsUsername": "{{serviceaccount_username}}",
    "VbcsPassword": "{{serviceaccount_password}}",
    "StsUserEndpoint": "https://{{your_instance_name}}.integration.ocp.oraclecloud.com/ic/builder/design/Skills_Task_Set_current_/1.0/resources/data/STSUser",
    "StsUserAddPayload": "{\"userEmail\":\"%USERNAME%\",\"firstName\":\"%FIRSTNAME%\",\"lastName\":\"%LASTNAME%\",\"path\":1,\"manager\":\"%MANAGER%\",\"roleName\":1}",
    "StsUpdateManagerPayload": "{\"manager\": \"%MANAGER%\"}"
}
```

##  Principles for API Usage
* Setting up IDCS with client application to retrieve JWT bearer tokens:  https://www.oracle.com/webfolder/technetwork/tutorials/obe/cloud/idcs/idcs_rest_1stcall_obe/rest_1stcall.html
 * Using IDCS APIs:
https://www.oracle.com/webfolder/technetwork/tutorials/obe/cloud/idcs/idcs_rest_users_obe/rest_users.html
* OIC REST API Reference:  https://docs.oracle.com/en/cloud/paas/identity-cloud/rest-api/
* Working with VBCS Business Object APIs:  https://docs.oracle.com/en/cloud/paas/app-builder-cloud/consume-rest/index.html

## Third Party Packages Used

 * Read-only JSON support:  https://github.com/tidwall/gjson
