# Copy Pasta

App Engine based app for copy/pasting from the browser to other devices, typically mobile. Uses Google+ sign in and is based on appengine-goplus and gplus-quickstart-go.

# Notes

Before running you'll need to setup a config file, I named mine config.go. It should be setup with your Google API OAuth2 details.

```go

package main

const (
	clientID        = "YOUR_CLIENT_ID"
	iosClientID     = "YOUR_IOS_CLIENT_ID"
	clientSecret    = "YOUR_CLIENT_SECRET"
	applicationName = "YOUR_APP_NAME"
)

```