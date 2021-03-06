// Copyright 2013 Google Inc. All Rights Reserved
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not
// use this file except in compliance with the License. You may obtain a copy of
// the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations under
// the License.

// Package main provides a simple server to demonstrate how to use Google+
// Sign-In and make a request via your own server.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"appengine"
	"appengine/datastore"
	"appengine/urlfetch"
	"code.google.com/p/goauth2/oauth"
	"github.com/gorilla/sessions"
)

// Update your Google API project information here.



// config is the configuration specification supplied to the OAuth package.
var config = &oauth.Config{
	ClientId:     clientID,
	ClientSecret: clientSecret,
	// Scope determines which API calls you are authorized to make
	Scope:    "https://www.googleapis.com/auth/plus.login",
	AuthURL:  "https://accounts.google.com/o/oauth2/auth",
	TokenURL: "https://accounts.google.com/o/oauth2/token",
	// Use "postmessage" for the code-flow for server side apps
	RedirectURL: "postmessage",
}

// store initializes the Gorilla session store.
var store = sessions.NewCookieStore([]byte(clientSecret)) //securecookie.GenerateRandomKey(32))

// indexTemplate is the HTML template we use to present the index page.
var indexTemplate = template.Must(template.ParseFiles("template/index.html"))

// Token represents an OAuth token response.
type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	IdToken     string `json:"id_token"`
}

// ClaimSet represents an IdToken response.
type ClaimSet struct {
	Sub string
}

type ValidationResponse struct {
	IssuedTo   string `json:"issued_to"`
	Audience   string `json:"audience"`
	UserId     string `json:"user_id"`
	Scope      string `json:"scope"`
	ExpiresIn  int    `json:"expires_in"`
	AccessType string `json:"access_type"`
}

//ya29.AHES6ZQ_CnvWsi-bmxoIPdruHd22CnA-a9F0Dt57lbLYodM
// validate a token, avoid the Confused Deputy Problem (http://en.wikipedia.org/wiki/Confused_deputy_problem)
func validateToken(token string, r *http.Request) (userId string, err error) {
	c := appengine.NewContext(r)
	client := urlfetch.Client(c)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v1/tokeninfo?access_token=" + token)
	if err != nil {
		return "", fmt.Errorf("Failed to validate token with error: %v", err)
	}

	defer resp.Body.Close()

	var vr ValidationResponse
	err = json.NewDecoder(resp.Body).Decode(&vr)

	if err != nil {
		return "", fmt.Errorf("Decoding validation response: %v", err)
	}

	c.Infof("Audience: %v, iosClientID: %v", vr.Audience, iosClientID)
	if vr.Audience != iosClientID {
		return "", errors.New("Validating token failed!")
	}

	return vr.UserId, nil
}

// exchange takes an authentication code and exchanges it with the OAuth
// endpoint for a Google API bearer token and a Google+ ID
func exchange(code string, r *http.Request) (accessToken string, idToken string, err error) {
	c := appengine.NewContext(r)
	client := urlfetch.Client(c)
	c.Infof("code: %v", code)
	// Exchange the authorization code for a credentials object via a POST request
	addr := "https://accounts.google.com/o/oauth2/token"
	values := url.Values{
		"Content-Type":  {"application/x-www-form-urlencoded"},
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {config.RedirectURL},
		"grant_type":    {"authorization_code"},
	}

	resp, err := client.PostForm(addr, values)
	if err != nil {
		return "", "", fmt.Errorf("Exchanging code: %v", err)
	}
	defer resp.Body.Close()

	// Decode the response body into a token object
	var token Token
	err = json.NewDecoder(resp.Body).Decode(&token)
	if err != nil {
		return "", "", fmt.Errorf("Decoding access token: %v", err)
	}

	c.Infof("token: %v\tuserId: %v", token.AccessToken, token.IdToken)
	return token.AccessToken, token.IdToken, nil
}

// decodeIdToken takes an ID Token and decodes it to fetch the Google+ ID within
func decodeIdToken(idToken string) (gplusID string, err error) {
	// An ID token is a cryptographically-signed JSON object encoded in base 64.
	// Normally, it is critical that you validate an ID token before you use it,
	// but since you are communicating directly with Google over an
	// intermediary-free HTTPS channel and using your Client Secret to
	// authenticate yourself to Google, you can be confident that the token you
	// receive really comes from Google and is valid. If your server passes the ID
	// token to other components of your app, it is extremely important that the
	// other components validate the token before using it.
	var set ClaimSet
	if idToken != "" {
		// Check that the padding is correct for a base64decode
		parts := strings.Split(idToken, ".")
		if len(parts) < 2 {
			return "", fmt.Errorf("Malformed ID token")
		}
		// Decode the ID token
		b, err := base64Decode(parts[1])
		if err != nil {
			return "", fmt.Errorf("Malformed ID token: %v", err)
		}
		err = json.Unmarshal(b, &set)
		if err != nil {
			return "", fmt.Errorf("Malformed ID token: %v", err)
		}
	}
	return set.Sub, nil
}

// index sets up a session for the current user and serves the index page
func index(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	// This check prevents the "/" handler from handling all requests by default
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Create a state token to prevent request forgery and store it in the session
	// for later validation
	session, err := store.Get(r, "sessionName")
	if err != nil {
		log.Println("error fetching session:", err)
		// Ignore the initial session fetch error, as Get() always returns a
		// session, even if empty.
	}
	state := randomString(64)
	session.Values["state"] = state
	session.Save(r, w)

	stateURL := url.QueryEscape(session.Values["state"].(string))

	// Fill in the missing fields in index.html
	var data = struct {
		ApplicationName, ClientID, State string
	}{applicationName, clientID, stateURL}

	// Render and serve the HTML
	err = indexTemplate.Execute(w, data)
	if err != nil {
		log.Println("error rendering template:", err)
		serveError(c, w, err)
	}
}

// connect exchanges the one-time authorization code for a token and stores the
// token in the session
func connect(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	// Ensure that the request is not a forgery and that the user sending this
	// connect request is the expected user
	session, err := store.Get(r, "sessionName")
	if err != nil {
		log.Println("error fetching session:", err)
		return
	}
	if r.FormValue("state") != session.Values["state"].(string) {
		serveError(c, w, errors.New("Invalid state parameter"))
		return
	}
	// Normally, the state is a one-time token; however, in this example, we want
	// the user to be able to connect and disconnect without reloading the page.
	// Thus, for demonstration, we don't implement this best practice.
	// session.Values["state"] = nil

	// Setup for fetching the code from the request payload
	x, err := ioutil.ReadAll(r.Body)
	if err != nil {
		serveError(c, w, err)
		return
	}
	code := string(x)

	accessToken, idToken, err := exchange(code, r)
	if err != nil {
		serveError(c, w, err)
		return
	}

	gplusID, err := decodeIdToken(idToken)
	if err != nil {
		serveError(c, w, err)
		return
	}

	// Check if the user is already connected
	storedToken := session.Values["accessToken"]
	storedGPlusID := session.Values["gplusID"]
	if storedToken != nil && storedGPlusID == gplusID {
		fmt.Fprint(w, "Connected")
		return
	}

	// Store the access token in the session for later use
	session.Values["accessToken"] = accessToken
	session.Values["gplusID"] = gplusID
	session.Save(r, w)
}

// disconnect revokes the current user's token and resets their session
func disconnect(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	client := urlfetch.Client(c)
	// Only disconnect a connected user
	session, err := store.Get(r, "sessionName")
	if err != nil {
		serveError(c, w, err)
		return
	}
	token := session.Values["accessToken"]
	if token == nil {
		serveError(c, w, errors.New("Current user not connected"))
		return
	}

	// Execute HTTP GET request to revoke current token
	url := "https://accounts.google.com/o/oauth2/revoke?token=" + token.(string)
	resp, err := client.Get(url)
	if err != nil {
		serveError(c, w, err)
		return
	}
	defer resp.Body.Close()

	// Reset the user's session
	session.Values["accessToken"] = nil
	session.Save(r, w)
}

type appError struct {
	Err     error
	Message string
	Code    int
}

// randomString returns a random string with the specified length
func randomString(length int) (str string) {
	b := make([]byte, length)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func base64Decode(s string) ([]byte, error) {
	// add back missing padding
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}

type Message struct {
	Identifier string
	Text       string
	Time       int64
}

func paste(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	authToken := r.Header.Get("Authorization")

	userId, err := validateToken(authToken, r)
	if err != nil {
		c.Infof("Error validating token: %v", err)
		serveError(c, w, err)
	}

	var m Message
	k := datastore.NewKey(c, "copies", userId, 0, nil)
	if err := datastore.Get(c, k, &m); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	w.Header().Set("Content-Type", "text/json; charset=utf-8")
	fmt.Fprint(w, string(b))
}

func copyForm(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	session, err := store.Get(r, "sessionName")
	if err != nil {
		serveError(c, w, err)
		return
	}

	storedGPlusID := session.Values["gplusID"]

	var m Message
	m.Identifier = storedGPlusID.(string)
	m.Text = r.FormValue("pasta")
	m.Time = time.Now().Unix()

	// Store the new post (no history yet, just store the latest thing)
	k := datastore.NewKey(c, "copies", m.Identifier, 0, nil)
	if _, err := datastore.Put(c, k, &m); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/json; charset=utf-8")
	fmt.Fprint(w, "Check your phone!")
}

func postCopy(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	p, err := ioutil.ReadAll(r.Body)
	if err != nil {
		// handler error
	}

	var m Message
	m.Time = time.Now().Unix()
	err = json.Unmarshal(p, &m)

	// Store the new post (no history yet, just store the latest thing)
	k := datastore.NewKey(c, "copies", m.Identifier, 0, nil)
	if _, err := datastore.Put(c, k, &m); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, "{\"success\": true}")
}

func init() {
	http.HandleFunc("/connect", connect)
	http.HandleFunc("/disconnect", disconnect)
	http.HandleFunc("/copy", postCopy)
	http.HandleFunc("/paste", paste)
	http.HandleFunc("/copyForm", copyForm)
	http.HandleFunc("/", index)
}

func serve404(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "404: Not Found")
}

func serveError(c appengine.Context, w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusInternalServerError)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "Internal Server Error")
	c.Errorf("%v", err)
}
