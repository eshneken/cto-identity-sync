//	CTO Tenancy Identity Synchronizer
//	Ed Shnekendorf, 2019, https://github.com/eshneken/cto-identity-synchronizer

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
	StsUserEndpoint           string
	StsUserAddPayload         string
	StsUpdateManagerPayload   string
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

// STS & ECAL app constants
const roleStsSolutionEngineer = "1"
const roleStsManager = "3"

func main() {
	// read system configuration from config file
	config := loadConfig("config.json")

	// determine if we are synchronizing or deleting users for this run
	deleteFlagSet := deleteOnThisRun()

	// create HTTP Client
	client := &http.Client{}

	// get IDCS bearer token
	fmt.Println("Authenticating to IDCS")
	accessToken := getIDCSAccessToken(config, client)

	// retrieve all person objects from bespoke Aria service
	fmt.Println("Calling Aria service to retrieve SE org")
	peopleList := getPeopleFromAria(config, client)
	fmt.Printf("Retrieved [%d] person entries from Aria Service\n", len(peopleList.Items))

	// This it the main control loop.  For each person returned from the service
	usersSucessfullyProcessed := 0
	for i, person := range peopleList.Items {
		fmt.Printf("*** Processing user [%d/%d] -> %s\n", i+1, len(peopleList.Items), person.DisplayName)

		// if we made it this far then the user has been fully added to IDCS, groups, and VBCS apps so count the success
		err := errors.New("")
		if deleteFlagSet {
			err = deleteUser(config, client, accessToken, person)
		} else {
			err = synchronizeUser(config, client, accessToken, person)
		}
		if err != nil {
			fmt.Println(err.Error())
		} else {
			usersSucessfullyProcessed++
		}

		// temporary break out
		if i >= 9 {
			fmt.Println("premature stop for testing!!!")
			fmt.Printf("*** Sucessfully processed [%d/%d] Users\n", usersSucessfullyProcessed, len(peopleList.Items))
			return
		}
	}
	fmt.Printf("*** Sucessfully processed [%d/%d] Users\n", usersSucessfullyProcessed, len(peopleList.Items))
}

//
// Synchronize a single user to IDCS/VBCS.  If a condition occurs that prevents this user from being added
// then return an error so that the calling function can continue on to the next user.
//
func synchronizeUser(config Config, client *http.Client, accessToken string, person AriaServicePerson) error {
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

	// add the user to the STS VBCS app user repository.  If the user exists, check the manager to make sure that
	// data is current and update if needed
	err = addUserToSTS(config, client, accessToken, person)
	if err != nil {
		fmt.Println("Error adding user to STS App, continuing to next user...")
		return err
	}

	return nil
}

//
// Delete a single user from IDCS/VBCS.  If a condition occurs that prevents this user from being deleting
// then return an error so that the calling function can continue on to the next user.
//
func deleteUser(config Config, client *http.Client, accessToken string, person AriaServicePerson) error {
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

	// get user from STS
	queryString = "q=userEmail='" + person.UserID + "'"
	req, _ = http.NewRequest("GET", config.StsUserEndpoint+"?"+queryString, nil)
	req.SetBasicAuth(config.VbcsUsername, config.VbcsPassword)
	res, err = client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		return errors.New(outputHTTPError("Get STS user by email", err, res))
	}
	json, _ = ioutil.ReadAll(res.Body)
	stsUserID := gjson.Get(string(json), "items.0.id")
	if len(stsUserID.String()) < 1 {
		return errors.New(outputHTTPError("Getting User ID from STS",
			fmt.Errorf("User Email [%s] not found in STS when trying to delete user [%s]",
				strings.TrimSpace(idcsUserID), person.DisplayName), res))
	}

	// delete user from STS
	req, _ = http.NewRequest("DELETE", config.StsUserEndpoint+"/"+stsUserID.String(), nil)
	req.SetBasicAuth(config.VbcsUsername, config.VbcsPassword)
	res, err = client.Do(req)
	if err != nil || res == nil || (res.StatusCode != 200 && res.StatusCode != 204) {
		return errors.New(outputHTTPError("Delete STS user", err, res))
	}

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
// Add the user to IDCS.  If they already exist a status of 409 (user already exists) will be
// returned which is fine and we return an empty id.  This way we will avoid first doing a lookup before attempting an add
//
func addUserToIDCS(config Config, client *http.Client, accessToken string, person AriaServicePerson) (string, error) {
	payload := strings.ReplaceAll(config.IdcsCreateNewUserPayload, "%USERNAME%", person.UserID)
	payload = strings.ReplaceAll(payload, "%FIRSTNAME%", person.FirstName)
	payload = strings.ReplaceAll(payload, "%LASTNAME%", person.LastName)

	req, _ := http.NewRequest("POST", config.IdcsBaseURL+"/admin/v1/Users", strings.NewReader(payload))
	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 201 {
		// 409 is expected if user already exists, don't throw an error
		if res.StatusCode != 409 {
			fmt.Println(outputHTTPError("Adding user to IDCS", err, res))
			return "", err
		}
	}
	defer res.Body.Close()

	json, _ := ioutil.ReadAll(res.Body)
	result := gjson.Get(string(json), "id")
	return result.String(), nil
}

//
// Try to add the user to the STS app.  For all new users, assume a role of "solution engineer" and a path of "None".
//
func addUserToSTS(config Config, client *http.Client, accessToken string, person AriaServicePerson) error {
	// first check to see if the user already exists by doing a search on their email in STS which is a
	// unique attribute
	queryString := "q=userEmail='" + person.UserID + "'"
	req, _ := http.NewRequest("GET", config.StsUserEndpoint+"?"+queryString, nil)
	req.SetBasicAuth(config.VbcsUsername, config.VbcsPassword)
	res, err := client.Do(req)
	if err != nil || res == nil || res.StatusCode != 200 {
		fmt.Println(outputHTTPError("Add User to STS -> Get user by email", err, res))
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
			payload := strings.ReplaceAll(config.StsUpdateManagerPayload, "%MANAGER%", person.Manager)

			req, _ = http.NewRequest("PATCH", config.StsUserEndpoint+"/"+personID.String(), strings.NewReader(payload))
			req.SetBasicAuth(config.VbcsUsername, config.VbcsPassword)
			req.Header.Add("Content-Type", "application/json")
			req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
			res, err := client.Do(req)
			if err != nil || res == nil || (res.StatusCode != 200 && res.StatusCode != 409) {
				fmt.Println(outputHTTPError("Add User to STS -> Update Manager", err, res))
				return err
			}
		}
	} else {
		// this block handles the case where the user does not exist in STS and needs to be added
		payload := strings.ReplaceAll(config.StsUserAddPayload, "%USERNAME%", person.UserID)
		payload = strings.ReplaceAll(payload, "%FIRSTNAME%", person.FirstName)
		payload = strings.ReplaceAll(payload, "%LASTNAME%", person.LastName)
		payload = strings.ReplaceAll(payload, "%MANAGER%", person.Manager)
		if person.NumberOfDirects > 0 {
			payload = strings.ReplaceAll(payload, "%ROLE%", roleStsManager)
		} else {
			payload = strings.ReplaceAll(payload, "%ROLE%", roleStsSolutionEngineer)
		}

		req, _ = http.NewRequest("POST", config.StsUserEndpoint, strings.NewReader(payload))
		req.SetBasicAuth(config.VbcsUsername, config.VbcsPassword)
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Content-Length", strconv.Itoa(len(payload)))
		res, err := client.Do(req)
		if err != nil || res == nil || (res.StatusCode != 201 && res.StatusCode != 200) {
			fmt.Println(outputHTTPError("Adding user to STS", err, res))
			return err
		}
	}

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
		fmt.Println("--add:  Synchronizes users from Aria service to IDCS/VBCS apps")
		fmt.Println("--delete:  Removes users returned from Aria service from IDCS/VBCS apps")
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
