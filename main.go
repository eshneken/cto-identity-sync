//	CTO Tenancy Identity Synchronizer
//	Ed Shnekendorf, 2019, https://github.com/eshneken/cto-identity-sync

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// Config holds all config data loaded from local config.json file
type Config struct {
	IdcsBaseURL               string
	IdcsClientID              string
	IdcsClientSecret          string
	IdcsCreateNewUserPayload  string
	IdcsAddUserToGroupPayload string
	AriaServiceEndpointURL    string
	ManagerGroupNames         string
	UserGroupNames            string
	VbcsUsername              string
	VbcsPassword              string
	EcalUserEndpoint          string
	EcalUserAddPayload        string
	EcalUpdateManagerPayload  string
	EcalUserRoleCode          string
	EcalManagerRoleCode       string
	OceBaseURL                string
	OceUsername               string
	OcePassword               string
	OceArtifactsFolderID      string
	OceAddUserPayload         string
}

// AriaServicePerson represents an individual returned from the custom Aria export service
type AriaServicePerson struct {
	UserID          string `json:"id"`
	LastName        string `json:"sn"`
	FirstName       string `json:"givenname"`
	Manager         string `json:"manager"`
	DisplayName     string `json:"displayname"`
	NumberOfDirects int    `json:"num_directs"`
}

// AriaServicePersonList represents an array of AriaServicePerson objcts
type AriaServicePersonList struct {
	Items []AriaServicePerson `json:"items"`
}

func main() {
	// read system configuration from config file
	config := loadConfig("config.json")

	// determine if we are synchronizing or deleting users for this run
	deleteFlagSet := deleteOnThisRun()

	// create HTTP Client
	client := &http.Client{}

	// retrieve all person objects from bespoke Aria service
	fmt.Println("Calling Aria service to retrieve SE org")
	peopleList := getPeopleFromAria(config, client)
	fmt.Printf("Retrieved [%d] person entries from Aria Service\n", len(peopleList.Items))

	// get IDCS bearer token
	fmt.Println("Authenticating to IDCS")
	accessToken := getIDCSAccessToken(config, client)

	//REMOVE after testing
	BREAKCOUNT := 1

	// Loop through all users and load/unload to IDCS/VBCS
	usersSucessfullyProcessed := 0
	println("*** Loop 1/2:  Synchronize with IDCS & VBCS")
	for i, person := range peopleList.Items {
		fmt.Printf("* Processing user [%d/%d] -> %s\n", i+1, len(peopleList.Items), person.DisplayName)

		// REMOVE AFTER TESTING:  Don't touch these accounts for now
		if person.LastName == "Kidwell" || person.LastName == "Sab" || person.LastName == "Shnekendorf" || person.LastName == "Kundu" || person.LastName == "Malli" {
			fmt.Println("Skipping user: " + person.DisplayName)
			continue
		}

		// if we made it this far then the user has been fully added to IDCS, groups, and VBCS apps so count the success
		err := errors.New("")
		if deleteFlagSet {
			err = deleteIDCSVBCSUser(config, client, accessToken, person)
		} else {
			err = addIDCSVBCSUser(config, client, accessToken, person)
		}
		if err != nil {
			fmt.Println(err.Error())
		} else {
			usersSucessfullyProcessed++
		}

		// REMOVE AFTER TESTING:  Stop at some fixed count
		if i >= BREAKCOUNT {
			fmt.Println("Premature stop for testing!!!")
			break
		}
	}
	fmt.Printf("*** Sucessfully processed [%d/%d] Users for IDCS/VBCS\n", usersSucessfullyProcessed, len(peopleList.Items))

	// sync OEC to IDCS
	println("*** Synchronizing IDCS to OEC in prep for second loop")
	syncErr := syncOCEProfileData(config.OceBaseURL, config.OceUsername, config.OcePassword, client)
	if syncErr != nil {
		println("Can't sync OCE profile repository so no point in trying to load/unload OCE.  EXITING....")
		os.Exit(1)
	}

	// loop through all users and load/unload into OCE
	usersSucessfullyProcessed = 0
	println("*** Loop 2/2:  Synchronize with OEC")
	for i, person := range peopleList.Items {
		fmt.Printf("* Processing user [%d/%d] -> %s\n", i+1, len(peopleList.Items), person.DisplayName)

		// REMOVE AFTER TESTING:  Don't touch these accounts for now
		if person.LastName == "Kidwell" || person.LastName == "Sab" || person.LastName == "Shnekendorf" || person.LastName == "Kundu" || person.LastName == "Malli" {
			fmt.Println("Skipping user: " + person.DisplayName)
			continue
		}

		err := errors.New("")
		if deleteFlagSet {
			err = deleteOCEUser(config, client, accessToken, person)
		} else {
			err = addOCEUser(config, client, accessToken, person)
		}
		if err != nil {
			fmt.Println(err.Error())
		} else {
			usersSucessfullyProcessed++
		}

		// REMOVE AFTER TESTING:  Stop at some fixed count
		if i >= BREAKCOUNT {
			fmt.Println("Premature stop for testing!!!")
			break
		}

	}
	fmt.Printf("*** Sucessfully processed [%d/%d] Users for OCE\n", usersSucessfullyProcessed, len(peopleList.Items))

}

//
// Add a single user to IDCS/VBCS.  If a condition occurs that prevents this user from being added
// then return an error so that the calling function can continue on to the next user.
//
func addOCEUser(config Config, client *http.Client, accessToken string, person AriaServicePerson) error {
	err := addUserToOCE(config.OceBaseURL, config.OceUsername, config.OcePassword, config.OceArtifactsFolderID,
		config.OceAddUserPayload, client, person)
	if err != nil {
		fmt.Println("Error adding user to OCE artifacts folder, continuing to next user...")
		return err
	}

	return nil
}

//
// Add a single user to IDCS/VBCS.  If a condition occurs that prevents this user from being added
// then return an error so that the calling function can continue on to the next user.
//
func addIDCSVBCSUser(config Config, client *http.Client, accessToken string, person AriaServicePerson) error {
	// Convert manager DN to email address
	person.Manager = convertManagerDnToEmail(person.Manager)

	// Adds user to IDCS and returns the user's unique IDCS ID.  If user cannot be added due to error or user already
	// existing then return empty string.  For now we will skip changing the user's group association and proceed just to
	// update them in VBCS
	addedUserID, err := addUserToIDCS(config, client, accessToken, person)
	if err != nil {
		fmt.Println("Error adding user to IDCS, continuing to next user...")
		return err
	}

	// if this is a new user, add the user to the correct IDCS groups based on whether they are an
	// employee or a manager.  If the user has already been previously added to IDCS then assume the groups
	// are correct.  As a sidenote, this clearly will break if a previously defined manager became an IC or vice
	// versa but we won't worry about that edge case for now since this should be a rare occurence.
	if len(addedUserID) > 0 {
		err = addUserToIDCSGroups(config, client, accessToken, person, addedUserID)
		if err != nil {
			fmt.Println("Error adding user to IDCS groups, continuing to next user...")
			return err
		}
	}

	// add the user to the ECAL VBCS app user repository.  If the user exists, check the manager to make sure that
	// data is current and update if needed
	err = addUserToVBCSApp("ECAL", config.EcalUserEndpoint, config.VbcsUsername, config.VbcsPassword,
		config.EcalUserAddPayload, config.EcalUpdateManagerPayload, config.EcalUserRoleCode, config.EcalManagerRoleCode,
		client, person)
	if err != nil {
		fmt.Println("Error adding user to ECAL App, continuing to next user...")
		return err
	}

	return nil
}

//
// Delete a single user from OEC.  If a condition occurs that prevents this user from being deleting
// then return an error so that the calling function can continue on to the next user.
//
func deleteOCEUser(config Config, client *http.Client, accessToken string, person AriaServicePerson) error {

	err := deleteUserFromOCE(config.OceBaseURL, config.OceUsername, config.OcePassword, config.OceArtifactsFolderID,
		config.OceAddUserPayload, client, person)
	if err != nil {
		fmt.Println("Error unmapping user from OCE artifacts folder, continuing to next user...")
		return err
	}

	// we so happy
	return nil
}

//
// Delete a single user from IDCS/VBCS.  If a condition occurs that prevents this user from being deleting
// then return an error so that the calling function can continue on to the next user.
//
func deleteIDCSVBCSUser(config Config, client *http.Client, accessToken string, person AriaServicePerson) error {
	// get user ID from IDCS
	queryString := url.QueryEscape("userName eq \"" + strings.TrimSpace(person.UserID) + "\"")
	req, _ := http.NewRequest("GET", config.IdcsBaseURL+"/admin/v1/Users?filter="+queryString, nil)
	req.Header.Add("Authorization", "Bearer "+accessToken)
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		return errors.New(outputHTTPError("Getting User ID from IDCS", err, res))
	}
	defer res.Body.Close()

	json, _ := ioutil.ReadAll(res.Body)
	result := gjson.Get(string(json), "Resources.0.id")
	idcsUserID := result.String()
	if len(idcsUserID) < 1 {
		return errors.New(outputHTTPError("Getting User ID from IDCS",
			fmt.Errorf("User Email [%s] not found in IDCS when trying to delete user [%s]",
				strings.TrimSpace(idcsUserID), person.DisplayName), res))
	}

	// delete user from IDCS and set the force flag since we want to automatically remove the user's group associations
	req, _ = http.NewRequest("DELETE", config.IdcsBaseURL+"/admin/v1/Users/"+idcsUserID+"?forceDelete=true", nil)
	req.Header.Add("Authorization", "Bearer "+accessToken)
	res, err = client.Do(req)
	if err != nil || res == nil || (res.StatusCode != 200 && res.StatusCode != 204) {
		return errors.New(outputHTTPError("Deleting user from IDCS", err, res))
	}

	// delete user from ECAL app
	err = deleteUserFromVBCSApp("ECAL", config.EcalUserEndpoint, config.VbcsUsername, config.VbcsPassword, client, accessToken, person)
	if err != nil {
		return err
	}

	// we so happy
	return nil
}

//
//  Adds the user to the appropriate IDCS groups based on whether they are an individual contributor or a manager.
//  The person record shows the number of direct reports so people with no directs get added to all the user groups
//  and persons with direct reports get added to all the manager groups
//
func addUserToIDCSGroups(config Config, client *http.Client, accessToken string, person AriaServicePerson, UserID string) error {
	// get either the individual (user) or manager group list
	groupList := config.UserGroupNames
	if person.NumberOfDirects > 0 {
		groupList = config.ManagerGroupNames
	}

	// for each group lets get the ID that corresponds to the group and then map the user to each group
	for _, groupName := range strings.Split(groupList, ",") {
		// get the group's IDCS ID based on group name
		queryString := url.QueryEscape("displayName eq \"" + strings.TrimSpace(groupName) + "\"")
		req, _ := http.NewRequest("GET", config.IdcsBaseURL+"/admin/v1/Groups?filter="+queryString, nil)
		req.Header.Add("Authorization", "Bearer "+accessToken)
		res, err := client.Do(req)
		if err != nil || res == nil || res.StatusCode != 200 {
			return errors.New(outputHTTPError("Getting Group ID from IDCS", err, res))
		}
		defer res.Body.Close()

		json, _ := ioutil.ReadAll(res.Body)
		result := gjson.Get(string(json), "Resources.0.id")
		groupID := result.String()
		if len(groupID) < 1 {
			return errors.New(outputHTTPError("Getting Group ID from IDCS",
				fmt.Errorf("Group Name [%s] not found in IDCS when trying to add user [%s]",
					strings.TrimSpace(groupName), person.DisplayName), res))
		}

		// add the user to the group
		payload := strings.ReplaceAll(config.IdcsAddUserToGroupPayload, "%USERID%", UserID)
		req, _ = http.NewRequest("PATCH",
			config.IdcsBaseURL+"/admin/v1/Groups/"+groupID, strings.NewReader(payload))
		req.Header.Add("Authorization", "Bearer "+accessToken)
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
		res, err = client.Do(req)
		if err != nil || res == nil || res.StatusCode != 200 {
			return errors.New(outputHTTPError("Adding user to IDCS", err, res))
		}
	}

	return nil
}

//
// Add the user to IDCS.  First check to see if they are already there and if they are then return their IDCS user ID
// If not, add them and return their IDCS user ID.  The IDCS userid will be used down the control flow to add them to groups
//
func addUserToIDCS(config Config, client *http.Client, accessToken string, person AriaServicePerson) (string, error) {
	// get user ID from IDCS
	queryString := url.QueryEscape("userName eq \"" + strings.TrimSpace(person.UserID) + "\"")
	req, _ := http.NewRequest("GET", config.IdcsBaseURL+"/admin/v1/Users?filter="+queryString, nil)
	req.Header.Add("Authorization", "Bearer "+accessToken)
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		return "", errors.New(outputHTTPError("Getting User ID from IDCS", err, res))
	}
	defer res.Body.Close()

	json, _ := ioutil.ReadAll(res.Body)
	result := gjson.Get(string(json), "Resources.0.id")
	idcsUserID := result.String()

	if len(idcsUserID) < 1 {
		payload := strings.ReplaceAll(config.IdcsCreateNewUserPayload, "%USERNAME%", person.UserID)
		payload = strings.ReplaceAll(payload, "%FIRSTNAME%", person.FirstName)
		payload = strings.ReplaceAll(payload, "%LASTNAME%", person.LastName)

		req, _ = http.NewRequest("POST", config.IdcsBaseURL+"/admin/v1/Users", strings.NewReader(payload))
		req.Header.Add("Authorization", "Bearer "+accessToken)
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
		res, err = client.Do(req)
		if err != nil || res == nil || res.StatusCode != 201 {
			// 409 is expected if user already exists, don't throw an error
			if res.StatusCode != 409 {
				fmt.Println(outputHTTPError("Adding user to IDCS", err, res))
				return "", err
			}
		}
		defer res.Body.Close()

		json, _ = ioutil.ReadAll(res.Body)
		result = gjson.Get(string(json), "id")
		idcsUserID = result.String()
	}

	return idcsUserID, nil
}

//
// Try to add the user to a VBCS app.
//
func addUserToVBCSApp(appName string, endpoint string, username string, password string, addUserTemplate string,
	replaceManagerTemplate string, userRole string, managerRole string, client *http.Client, person AriaServicePerson) error {
	// first check to see if the user already exists by doing a search on their email in VBCS which is a
	// unique attribute
	queryString := "q=userEmail='" + person.UserID + "'"
	req, _ := http.NewRequest("GET", endpoint+"?"+queryString, nil)
	req.SetBasicAuth(username, password)
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		fmt.Println(outputHTTPError("Add User to "+appName+" -> Get user by email", err, res))
		return err
	}
	defer res.Body.Close()

	// get the internal person ID from VBCS and their manager email
	json, _ := ioutil.ReadAll(res.Body)
	personID := gjson.Get(string(json), "items.0.id")
	managerID := gjson.Get(string(json), "items.0.manager")

	// if a userid was returned then the person already exists.
	if len(personID.String()) > 0 {
		// this block handles the case where the user already exists and we check to see if the manager
		// email needs to be updated
		if managerID.String() != person.Manager {
			payload := strings.ReplaceAll(replaceManagerTemplate, "%MANAGER%", person.Manager)

			req, _ = http.NewRequest("PATCH", endpoint+"/"+personID.String(), strings.NewReader(payload))
			req.SetBasicAuth(username, password)
			req.Header.Add("Content-Type", "application/json")
			req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
			res, err := client.Do(req)
			if err != nil || res == nil || (res.StatusCode != 200 && res.StatusCode != 409) {
				fmt.Println(outputHTTPError("Add User to "+appName+" -> Update Manager", err, res))
				return err
			}
		}
	} else {
		// this block handles the case where the user does not exist in VBCS and needs to be added
		payload := strings.ReplaceAll(addUserTemplate, "%USERNAME%", person.UserID)
		payload = strings.ReplaceAll(payload, "%FIRSTNAME%", person.FirstName)
		payload = strings.ReplaceAll(payload, "%LASTNAME%", person.LastName)
		payload = strings.ReplaceAll(payload, "%MANAGER%", person.Manager)
		if person.NumberOfDirects > 0 {
			payload = strings.ReplaceAll(payload, "%ROLE%", managerRole)
		} else {
			payload = strings.ReplaceAll(payload, "%ROLE%", userRole)
		}

		req, _ = http.NewRequest("POST", endpoint, strings.NewReader(payload))
		req.SetBasicAuth(username, password)
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
		res, err := client.Do(req)
		if err != nil || res == nil || (res.StatusCode != 201 && res.StatusCode != 200) {
			fmt.Println(outputHTTPError("Adding user to "+appName, err, res))
			return err
		}
	}

	return nil
}

//
// Synchronize OEC user/profile data with IDCS.  This is a costly operation so should only be executed once
// after all user changes have been made in IDCS but before any activity can be initiated for user mapping in
// OCE
//
func syncOCEProfileData(endpoint string, username string, password string, client *http.Client) error {
	req, _ := http.NewRequest("POST", endpoint+"/documents/integration/ecal?IdcService=SYNC_USERS_AND_ATTRIBUTES", nil)
	req.SetBasicAuth(username, password)
	req.Header.Add("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		fmt.Println(outputHTTPError("Sync Profile Data", err, res))
		return err
	}
	defer res.Body.Close()
	return nil // we so happy
}

//
// Try to add the user to an OCE content folder as a downloader
//
func addUserToOCE(endpoint string, username string, password string, folderID string, addUserPayload string,
	client *http.Client, person AriaServicePerson) error {

	// get the OCE user id by their email
	queryString := "email=" + person.UserID
	req, _ := http.NewRequest("GET", endpoint+"/documents/api/1.2/users/search/items?"+queryString, nil)
	req.SetBasicAuth(username, password)
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		fmt.Println(outputHTTPError("Add User to OCE -> Get user by email", err, res))
		return err
	}
	defer res.Body.Close()

	// get the internal person ID from OCE;  if no id return throw an error
	json, _ := ioutil.ReadAll(res.Body)
	personID := gjson.Get(string(json), "items.0.id")
	if len(personID.String()) < 1 {
		err = errors.New("No ID returned; OCE not synced with this user")
		fmt.Println(outputHTTPError("Add User to OCE -> Get OCE id from email ["+person.UserID+"]", err, res))
		return err
	}

	// Add person as downloader for the Artifacts folder
	payload := strings.ReplaceAll(addUserPayload, "%USERNAME%", personID.String())
	req, _ = http.NewRequest("POST", endpoint+"/documents/api/1.2/shares/"+folderID, strings.NewReader(payload))
	req.SetBasicAuth(username, password)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
	res, err = client.Do(req)
	if err != nil || res == nil {
		fmt.Println(outputHTTPError("Add User to OCE -> Get user by email", err, res))
		return err
	}

	// check the error code.  If the user has already been added to the folder then squelch the error and continue on
	if res.StatusCode != 200 {
		returnBody, _ := ioutil.ReadAll(res.Body)
		errorKey := gjson.Get(string(returnBody), "errorKey")
		err = errors.New(string(returnBody))
		if !strings.HasPrefix(errorKey.String(), "!csFolderAlreadyShared") {
			fmt.Println(outputHTTPError("Add User to OCE -> Add user as downloader to artifacts folder",
				err, res))
			return err
		}
	}
	return nil // me so happy
}

//
// Remove user as downloader from OCE folder
//
func deleteUserFromOCE(endpoint string, username string, password string, folderID string, deleteUserPayload string,
	client *http.Client, person AriaServicePerson) error {

	// get the OCE user id by their email
	queryString := "email=" + person.UserID
	req, _ := http.NewRequest("GET", endpoint+"/documents/api/1.2/users/search/items?"+queryString, nil)
	req.SetBasicAuth(username, password)
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		fmt.Println(outputHTTPError("Delete user from OCE -> Get user by email", err, res))
		return err
	}
	defer res.Body.Close()

	// get the internal person ID from VBCS and their manager email
	json, _ := ioutil.ReadAll(res.Body)
	personID := gjson.Get(string(json), "items.0.id")

	// Add person as downloader for the Artifacts folder
	payload := strings.ReplaceAll(deleteUserPayload, "%USERNAME%", personID.String())
	req, _ = http.NewRequest("DELETE", endpoint+"/documents/api/1.2/shares/"+folderID+"/user", strings.NewReader(payload))
	req.SetBasicAuth(username, password)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
	res, err = client.Do(req)
	if err != nil || res == nil {
		fmt.Println(outputHTTPError("Delete user from OCE -> Remove user as downloader to artifacts folder", err, res))
		return err
	}

	// check the error code.  If the user has already been removed from the folder then squelch the error and continue on
	if res.StatusCode != 200 {
		returnBody, _ := ioutil.ReadAll(res.Body)
		errorKey := gjson.Get(string(returnBody), "errorKey")
		err = errors.New(string(returnBody))
		if !strings.HasPrefix(errorKey.String(), "!csUserHasNotBeenShared") {
			fmt.Println(outputHTTPError("Remove user from OCE -> Remove user as downloader to artifacts folder",
				err, res))
			return err
		}

		println("User [" + person.DisplayName + "] already unshared from OEC folder")
	}
	return nil // me so happy
}

//
// Delete user from VBCS app.
//
func deleteUserFromVBCSApp(appName string, endpoint string, username string, password string,
	client *http.Client, accessToken string, person AriaServicePerson) error {

	// get user from VBCS app
	queryString := "q=userEmail='" + person.UserID + "'"
	req, _ := http.NewRequest("GET", endpoint+"?"+queryString, nil)
	req.SetBasicAuth(username, password)
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		return errors.New(outputHTTPError("Get "+appName+" user by email", err, res))
	}
	defer res.Body.Close()

	json, _ := ioutil.ReadAll(res.Body)
	vbcsUserID := gjson.Get(string(json), "items.0.id")
	if len(vbcsUserID.String()) < 1 {
		return errors.New(outputHTTPError("Getting User ID from "+appName,
			fmt.Errorf("User Email [%s] not found in "+appName+" when trying to delete user [%s]",
				strings.TrimSpace(person.UserID), person.DisplayName), res))
	}

	// delete user from VBCS app
	req, _ = http.NewRequest("DELETE", endpoint+"/"+vbcsUserID.String(), nil)
	req.SetBasicAuth(username, password)
	res, err = client.Do(req)
	if err != nil || res == nil || (res.StatusCode != 200 && res.StatusCode != 204) {
		return errors.New(outputHTTPError("Delete "+appName+" user", err, res))
	}

	// we so happy
	return nil
}

//
// Authenticate to IDCS and retrieve OAuth2 bearer token that will be used for this session to communicate
// with IDCS.  Any errors cause us to panic here since we can't proceed further
//
func getIDCSAccessToken(config Config, client *http.Client) string {
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("scope", "urn:opc:idm:__myscopes__")

	req, _ := http.NewRequest("POST", config.IdcsBaseURL+"/oauth2/v1/token", strings.NewReader(data.Encode()))
	req.SetBasicAuth(config.IdcsClientID, config.IdcsClientSecret)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("Content-Length", strconv.Itoa(len(data.Encode())))

	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		panic(outputHTTPError("Getting IDCS bearer token", err, res))
	}
	defer res.Body.Close()

	json, _ := ioutil.ReadAll(res.Body)
	accessToken := gjson.Get(string(json), "access_token")
	if len(accessToken.String()) < 1 {
		panic("IDCS bearer token not retrieved")
	}

	return accessToken.String()
}

//
// Authenticate to IDCS and retrieve OAuth2 bearer token that will be used for this session to communicate
// with OCE.  Any errors cause us to panic here since we can't proceed further
//
func getOCEAccessToken(config Config, client *http.Client) string {
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("scope", "urn:opc:cec:all ")

	req, _ := http.NewRequest("POST", config.IdcsBaseURL+"/oauth2/v1/token", strings.NewReader(data.Encode()))
	req.SetBasicAuth(config.IdcsClientID, config.IdcsClientSecret)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("Content-Length", strconv.Itoa(len(data.Encode())))

	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		panic(outputHTTPError("Getting IDCS bearer token", err, res))
	}
	defer res.Body.Close()

	json, _ := ioutil.ReadAll(res.Body)
	accessToken := gjson.Get(string(json), "access_token")
	if len(accessToken.String()) < 1 {
		panic("IDCS bearer token not retrieved")
	}

	return accessToken.String()
}

// Call Aria service to get a list of all people.  If we get an error then panic here since we can't proceed further
//
func getPeopleFromAria(config Config, client *http.Client) AriaServicePersonList {
	req, _ := http.NewRequest("GET", config.AriaServiceEndpointURL, nil)
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		panic(outputHTTPError("Getting Aria Person List", err, res))
	}
	defer res.Body.Close()
	peopleList := AriaServicePersonList{}
	json.NewDecoder(res.Body).Decode(&peopleList)
	return peopleList
}

//
//  Read the config.json file and parse configuration data into a struct.  On error, panic here.
//
func loadConfig(filename string) Config {
	var config = Config{}
	file, err := os.Open(filename)
	if err != nil {
		panic(err.Error())
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		panic(err.Error())
	}

	return config
}

//
// Generic error formatting message for HTTP operations
//
func outputHTTPError(message string, err error, res *http.Response) string {
	if err != nil {
		return fmt.Sprintf("ERROR: %s: %s", message, err.Error())
	} else if res == nil {
		return fmt.Sprintf("ERROR: %s: %s", message, "HTTP Response is nil")
	} else {
		json, _ := ioutil.ReadAll(res.Body)
		return fmt.Sprintf("ERROR: %s: %s: detail ->%s", message, res.Status, string(json))
	}
}

//
// Convert a LDAP DN of form (cn=FIRST_NAME,l=amer,dc=oracle,dc=com) to an email of form first.name@oracle.com
//
func convertManagerDnToEmail(managerDN string) string {
	if len(managerDN) < 1 {
		return ""
	}

	dnComponents := strings.Split(managerDN, ",")
	if len(dnComponents) < 1 {
		return ""
	}

	email := strings.ToLower(strings.ReplaceAll(dnComponents[0], "_", "."))
	cnComponents := strings.Split(email, "=")
	email = cnComponents[1] + "@oracle.com"
	return email
}

//
// Determines whether this run should add or delete users from IDCS/VBCS.  Returns true to delete
// and false to add (sets a delete flag on the main loop).  If --help or -h is passed in outputs
// help to the command line
//
func deleteOnThisRun() bool {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Printf("Usage: %s [--help || --add || --delete]\n", os.Args[0])
		fmt.Println("--help:  Prints this message")
		fmt.Println("--add:  Synchronizes users from Aria service to IDCS/VBCS/OCE apps")
		fmt.Println("--delete:  Removes users returned from Aria service from IDCS/VBCS/OCE apps")
		os.Exit(1)
	}

	if os.Args[1] == "--delete" {
		fmt.Println("Starting user DELETION flow")
		return true
	} else if os.Args[1] == "--add" {
		fmt.Println("Starting user ADDITION flow")
		return false
	} else {
		fmt.Printf("Missing command line arguments.  Try %s --help\n", os.Args[0])
		os.Exit(3)
	}

	return true // this return should never be reached
}

//
// Helper function to print response body as a string
//
func printBody(res *http.Response) {
	bodyBytes, _ := ioutil.ReadAll(res.Body)
	fmt.Println(string(bodyBytes))
}
