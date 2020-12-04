//	CTO Tenancy Identity Synchronizer
//	Ed Shnekendorf, 2020, https://github.com/eshneken/cto-identity-sync

package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/common"
	"github.com/oracle/oci-go-sdk/common/auth"
	"github.com/oracle/oci-go-sdk/secrets"
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
	AriaServiceUsername       string
	AriaServicePassword       string
	ManagerGroupNames         string
	UserGroupNames            string
	VbcsUsername              string
	VbcsPassword              string
	EcalUserEndpoint          string
	EcalUserAddPayload        string
	EcalUpdateManagerPayload  string
	EcalUserRoleCode          string
	EcalManagerRoleCode       string
	StsUserEndpoint           string
	StsUserAddPayload         string
	StsUpdateManagerPayload   string
	StsUserRoleCode           string
	StsManagerRoleCode        string
	OceBaseURL                string
	OceUsername               string
	OcePassword               string
	OceArtifactsFolderID      string
	OceAddUserPayload         string
}

// AriaServicePerson represents an individual returned from the corporate identity feed
type AriaServicePerson struct {
	UserID          string `json:"id"`
	LastName        string `json:"sn"`
	FirstName       string `json:"givenname"`
	Manager         string `json:"manager"`
	DisplayName     string `json:"displayname"`
	Lob             string `json:"lob"`
	LobParent       string `json:"lob_parent"`
	NumberOfDirects int    `json:"num_directs"`
	AppMap          string `json:"app_map"`
}

// AriaServicePersonList represents an array of AriaServicePerson objcts
type AriaServicePersonList struct {
	Items []AriaServicePerson `json:"items"`
}

// ADD argument for add mode
const ADD = "--add"

// DELETE argument for delete mode
const DELETE = "--delete"

// CLEAN argument for clean mode
const CLEAN = "--clean"

// LIST argument for list mode
const LIST = "--list"

func main() {
	println("Invocation Start: " + time.Now().Format(time.RFC3339))

	// determine if we are synchronizing or deleting users for this run
	var runMode string
	runMode = invocationRunMode()

	// read system configuration from config file
	config := loadConfig("config.json")

	// create HTTP Client
	client := &http.Client{}

	// Get IDCS accessToken
	accessToken := getIDCSAccessToken(config, client)

	// retrieve all person objects from corporate identity feed
	fmt.Println("Calling corporate identity feed to retrieve SE org")
	peopleList := getPeopleFromAria(config, client)
	fmt.Printf("Retrieved [%d] person entries from corporate identity feed\n", len(peopleList.Items))

	//REMOVE after testing
	//BREAKCOUNT := 2

	// Loop through all users and load/unload to IDCS/VBCS
	usersSucessfullyProcessed := 0
	if runMode == LIST || runMode == ADD || runMode == DELETE {
		if runMode == LIST {
			println("*** Loop 1/1:  List all corporate identities")
		} else {
			println("*** Loop 1/2:  Synchronize with IDCS & VBCS")
		}

		for i, person := range peopleList.Items {
			// get a new IDCS access token if we've processed 1000 users.  Access tokens last 60 minutes and experimentally
			// processing of around 1500 users with current APIs and hardware seems to take about one hour.  So to avoid a
			// token timeout grab a new token every 1000 processed users.
			if i > 0 && i%1000 == 0 {
				fmt.Println("** Refreshing IDCS OAuth access token...")
				accessToken = getIDCSAccessToken(config, client)
			}

			if runMode != LIST {
				fmt.Printf("* Processing user [%d/%d] -> %s\n", i+1, len(peopleList.Items), person.DisplayName)
			}

			// if we made it this far then the user has been fully added to IDCS, groups, and VBCS apps so count the success
			err := errors.New("")
			if runMode == LIST {
				fmt.Printf("** name=%s, email=%s, num_directs=%d, manager=%s", person.DisplayName, person.UserID, person.NumberOfDirects, person.Manager)
			}
			if runMode == DELETE {
				err = deleteIDCSVBCSUser(config, client, accessToken, person)
			}
			if runMode == ADD {
				err = addIDCSVBCSUser(config, client, accessToken, person)
			}

			if err != nil {
				fmt.Println(err.Error())
			} else {
				usersSucessfullyProcessed++
			}

			// REMOVE AFTER TESTING:  Stop at some fixed count
			/*
				if i >= BREAKCOUNT {
					fmt.Println("Premature stop for testing!!!")
					break
				}
			*/
		}
		fmt.Printf("*** Sucessfully processed [%d/%d] Users for IDCS/VBCS (%s) \n", usersSucessfullyProcessed, len(peopleList.Items), time.Now().Format(time.RFC3339))
	}

	if runMode == ADD || runMode == DELETE {
		// sync OEC to IDCS
		println("*** Synchronizing IDCS to OCE in prep for second loop")
		syncErr := syncOCEProfileData(config.OceBaseURL, config.OceUsername, config.OcePassword, client)
		if syncErr != nil {
			println("Can't sync OCE profile repository so no point in trying to load/unload OCE.  EXITING....")
			os.Exit(1)
		}

		// loop through all users and load/unload into OCE
		usersSucessfullyProcessed = 0
		println("*** Loop 2/2:  Synchronize with OCE")
		for i, person := range peopleList.Items {
			fmt.Printf("* Processing user [%d/%d] -> %s\n", i+1, len(peopleList.Items), person.DisplayName)

			if strings.Contains(person.AppMap, "ECAL") {
				err := errors.New("")
				if runMode == DELETE {
					err = deleteOCEUser(config, client, accessToken, person)
				}
				if runMode == ADD {
					err = addOCEUser(config, client, accessToken, person)
				}
				if err != nil {
					fmt.Println(err.Error())
				} else {
					usersSucessfullyProcessed++
				}
			} else {
				fmt.Printf("** Skipping user, OCE is only for ECAL application mappings...\n")
			}

			// REMOVE AFTER TESTING:  Stop at some fixed count
			/*
				if i >= BREAKCOUNT {
					fmt.Println("Premature stop for testing!!!")
					break
				}
			*/
		}
		fmt.Printf("*** Sucessfully processed [%d/%d] Users for OCE (%s)\n", usersSucessfullyProcessed, len(peopleList.Items), time.Now().Format(time.RFC3339))
	}

	if runMode == CLEAN {
		println("*** Loop 1/1:  Clean users from IDCS/VBCS/OCE not in corporate identity feed")

		// convert the personList to a hashmap for efficient searching
		ariaMap := make(map[string]AriaServicePerson)
		for _, person := range peopleList.Items {
			ariaMap[person.UserID] = person
		}

		// get all users from ECAL app
		req, _ := http.NewRequest("GET", config.EcalUserEndpoint+"?limit=5000&fields=userEmail&onlyData=true", nil)
		req.SetBasicAuth(config.VbcsUsername, config.VbcsPassword)
		res, err := client.Do(req)
		if err != nil || res == nil || res.StatusCode != 200 {
			fmt.Println(outputHTTPError("Get all users from ECAL app", err, res))
			os.Exit(3)
		}
		defer res.Body.Close()

		json, _ := ioutil.ReadAll(res.Body)
		result := gjson.Get(string(json), "items.#.userEmail").Array()
		removeCount := 0
		for _, email := range result {
			person, userExistsInAria := ariaMap[email.String()]
			if !userExistsInAria && !strings.Contains(email.String(), "cto-test") {
				fmt.Printf("** User [" + email.String() + "] not found in corporate identity feed.  Remove [y/n]?")

				// confirm removal by reading response from console
				text, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				person.UserID = email.String()
				person.DisplayName = email.String()
				text = strings.Replace(text, "\n", "", -1)

				if strings.Compare("Y", strings.ToUpper(text)) == 0 {
					println("*** Removing user [" + email.String() + "]")
					// remove user from VBCS
					err = deleteIDCSVBCSUser(config, client, accessToken, person)
					if err != nil {
						fmt.Println(err.Error())
					}

					// remove user from OCE
					err2 := deleteOCEUser(config, client, accessToken, person)
					if err2 != nil {
						fmt.Println(err2.Error())
					}

					// up the success count
					if err == nil && err2 == nil {
						removeCount++
					}
				} else {
					println("*** Skipping removal of user [" + email.String() + "]")
				}
			}
		}
		fmt.Printf("*** Removed %d users from IDCS/VBCS/OCE\n", removeCount)
	}
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
	/*
		if strings.Contains(person.AppMap, "ECAL") {
			err = addUserToVBCSApp("ECAL", config.EcalUserEndpoint, config.VbcsUsername, config.VbcsPassword,
				config.EcalUserAddPayload, config.EcalUpdateManagerPayload, config.EcalUserRoleCode, config.EcalManagerRoleCode,
				client, person)
			if err != nil {
				fmt.Println("Error adding user to ECAL App, continuing to next user...")
				return err
			}
		}
	*/

	// add the user to the ECAL VBCS app user repository.  If the user exists, check the manager to make sure that
	// data is current and update if needed
	if strings.Contains(person.AppMap, "STS") {
		err = addUserToVBCSApp("STS", config.StsUserEndpoint, config.VbcsUsername, config.VbcsPassword,
			config.StsUserAddPayload, config.StsUpdateManagerPayload, config.StsUserRoleCode, config.StsManagerRoleCode,
			client, person)
		if err != nil {
			fmt.Println("Error adding user to STS App, continuing to next user...")
			return err
		}
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

	// delete user from STS app
	err = deleteUserFromVBCSApp("STS", config.StsUserEndpoint, config.VbcsUsername, config.VbcsPassword, client, accessToken, person)
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
	updateUserTemplate string, userRole string, managerRole string, client *http.Client, person AriaServicePerson) error {
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

	// if a userid was returned then the person already exists.  In case a manager, name, or role changed we make
	// the decision to just update all users in VBCS every time to keep things clean.
	if len(personID.String()) > 0 {
		// this block handles the case where the user needs to be updated
		payload := strings.ReplaceAll(updateUserTemplate, "%USERNAME%", person.UserID)
		payload = strings.ReplaceAll(payload, "%FIRSTNAME%", person.FirstName)
		payload = strings.ReplaceAll(payload, "%LASTNAME%", person.LastName)
		payload = strings.ReplaceAll(payload, "%MANAGER%", person.Manager)
		payload = strings.ReplaceAll(payload, "%LOB%", person.Lob)
		payload = strings.ReplaceAll(payload, "%LOBPARENT%", person.LobParent)
		if person.NumberOfDirects > 0 {
			payload = strings.ReplaceAll(payload, "%ROLE%", managerRole)
		} else {
			payload = strings.ReplaceAll(payload, "%ROLE%", userRole)
		}

		req, _ = http.NewRequest("PATCH", endpoint+"/"+personID.String(), strings.NewReader(payload))
		req.SetBasicAuth(username, password)
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
		res, err := client.Do(req)
		if err != nil || res == nil || (res.StatusCode != 200 && res.StatusCode != 409) {
			fmt.Println(outputHTTPError("Add User to "+appName+" -> Update User", err, res))
			return err
		}
	} else {
		// this block handles the case where the user does not exist in VBCS and needs to be added
		payload := strings.ReplaceAll(addUserTemplate, "%USERNAME%", person.UserID)
		payload = strings.ReplaceAll(payload, "%FIRSTNAME%", person.FirstName)
		payload = strings.ReplaceAll(payload, "%LASTNAME%", person.LastName)
		payload = strings.ReplaceAll(payload, "%MANAGER%", person.Manager)
		payload = strings.ReplaceAll(payload, "%LOB%", person.Lob)
		payload = strings.ReplaceAll(payload, "%LOBPARENT%", person.LobParent)
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
			fmt.Println(outputHTTPError("Adding user to "+appName+" -> Add New User", err, res))
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

// Call corporate identity feed to get a list of all people.  If we get an error then panic here since we can't proceed further
//
func getPeopleFromAria(config Config, client *http.Client) AriaServicePersonList {
	req, _ := http.NewRequest("GET", config.AriaServiceEndpointURL, nil)
	req.SetBasicAuth(config.AriaServiceUsername, config.AriaServicePassword)
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		outputHTTPError("Getting corporate identity list", err, res)
		panic("exiting")
	}
	defer res.Body.Close()
	peopleList := AriaServicePersonList{}
	json.NewDecoder(res.Body).Decode(&peopleList)
	return peopleList
}

//
//  Read the config.json file and parse configuration data into a struct. Communicate with the OCI Secrets Service
//  to retrieve the secret data. On error, panic here.
//
func loadConfig(filename string) Config {

	// open the config file
	var config = Config{}
	file, err := os.Open(filename)
	if err != nil {
		panic("reading config.json: " + err.Error())
	}
	defer file.Close()

	// decode config.json into struct
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		panic("marshalling to struct: " + err.Error())
	}

	// connect to the OCI Secrets Service
	var provider common.ConfigurationProvider
	provider, err = auth.InstancePrincipalConfigurationProvider()
	if err != nil {
		provider = common.DefaultConfigProvider()
	}

	client, err := secrets.NewSecretsClientWithConfigurationProvider(provider)
	if err != nil {
		panic("connecting to OCI Secrets Service: " + err.Error())
	}

	// step through all the struct values and scan for [vault] prefix
	// which indicates that the value needs to be retrieved from the OCI Secret Service
	// format is [vault]FieldName:OCID
	v := reflect.ValueOf(config)
	values := make([]interface{}, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		values[i] = v.Field(i).Interface()
		if strings.HasPrefix(values[i].(string), "[vault]") {
			keySlice := strings.Split(strings.TrimPrefix(values[i].(string), "[vault]"), ":")
			fieldName := keySlice[0]
			vaultKey := keySlice[1]
			vaultValue := getSecretValue(client, vaultKey)
			reflect.ValueOf(&config).Elem().FieldByName(fieldName).SetString(vaultValue)
		}
	}

	return config
}

//
// Returns a secret value from the OCI Secret Service based on a secret OCID
//
func getSecretValue(client secrets.SecretsClient, secretOCID string) string {
	request := secrets.GetSecretBundleRequest{SecretId: &secretOCID}
	response, err := client.GetSecretBundle(context.Background(), request)
	if err != nil {
		panic("reading value for key [" + secretOCID + "]: " + err.Error())
	}

	encodedResponse := fmt.Sprintf("%s", response.SecretBundleContent)
	encodedResponse = strings.TrimRight(strings.TrimLeft(encodedResponse, "{ Content="), " }")
	decodedByteArray, err := base64.StdEncoding.DecodeString(encodedResponse)
	if err != nil {
		panic("decoding value for key [" + secretOCID + "]: " + err.Error())
	}

	return string(decodedByteArray)
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
// Determines what mode this invocation should run in.  Returns a constant value based on the argument detection
// that should be used for comparison in the main control flow.  If --help or -h is passed in outputs
// help to the command line.
//
func invocationRunMode() string {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Printf("Usage: %s [--help || --add || --delete]\n", os.Args[0])
		fmt.Println("--help:    Prints this message")
		fmt.Println("--add:     Synchronizes users from the corporate identity feed to IDCS/VBCS/OCE apps")
		fmt.Println("--delete:  Removes all users returned from the corporate identity feed from IDCS/VBCS/OCE apps")
		fmt.Println("--clean:   Removes users from IDCS/VBCS/OCE apps who are no longer found in the corporate identity feed.  This should be run interactively since it requires console confirmation for each user to be deleted.")
		fmt.Println("--list:    List all user data retrieved from the corporate identity feed")
		os.Exit(1)
	}

	if os.Args[1] == DELETE {
		fmt.Println("Starting user DELETION flow")
		return DELETE
	} else if os.Args[1] == ADD {
		fmt.Println("Starting user ADDITION flow")
		return ADD
	} else if os.Args[1] == CLEAN {
		fmt.Println("Starting user CLEAN flow")
		return CLEAN
	} else if os.Args[1] == LIST {
		fmt.Println("Starting user LIST flow")
		return LIST
	} else {
		fmt.Printf("Missing command line arguments.  Try %s --help\n", os.Args[0])
		os.Exit(3)
	}

	return "" // this return should never be reached
}

//
// Helper function to print response body as a string
//
func printBody(res *http.Response) {
	bodyBytes, _ := ioutil.ReadAll(res.Body)
	fmt.Println(string(bodyBytes))
}
