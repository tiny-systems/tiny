package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/authstore"
)

// defaultIssuer is the platform's Keycloak realm. The `tiny-cli` client is
// public with the device authorization grant enabled — no secret involved.
const defaultIssuer = "https://auth.tinysystems.io/realms/tinysystems"

const oidcClientID = "tiny-cli"

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func newLoginCmd() *cobra.Command {
	var issuer string
	var workspace string
	c := &cobra.Command{
		Use:   "login",
		Short: "Sign in to tinysystems.io (OIDC device flow)",
		Long: `Login opens a browser page where you approve this device once; tokens are
stored in your user config dir (0600) and refreshed automatically. Used by
commands that talk to the platform, like 'tiny publish'.

Robots should keep using developer keys (TINY_API_KEY) instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			httpc := &http.Client{Timeout: 30 * time.Second}

			// 1. device code
			resp, err := httpc.PostForm(issuer+"/protocol/openid-connect/auth/device",
				url.Values{"client_id": {oidcClientID}, "scope": {"openid profile email"}})
			if err != nil {
				return err
			}
			var dc deviceCodeResponse
			if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
				resp.Body.Close()
				return err
			}
			resp.Body.Close()
			if dc.DeviceCode == "" {
				return fmt.Errorf("device authorization failed — is the tiny-cli client enabled on %s?", issuer)
			}

			verify := dc.VerificationURIComplete
			if verify == "" {
				verify = dc.VerificationURI
			}
			fmt.Printf("\n  Open to approve this device:\n\n    %s\n\n  Code: %s\n\n", styleTitle.Render(verify), dc.UserCode)
			openBrowser(verify)

			// 2. poll
			interval := time.Duration(max(dc.Interval, 5)) * time.Second
			deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
			for {
				if time.Now().After(deadline) {
					return fmt.Errorf("login timed out — run `tiny login` again")
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(interval):
				}

				tr, err := postToken(httpc, issuer, url.Values{
					"client_id":   {oidcClientID},
					"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
					"device_code": {dc.DeviceCode},
				})
				if err != nil {
					return err
				}
				switch tr.Error {
				case "":
					creds := &authstore.Credentials{
						AccessToken:  tr.AccessToken,
						RefreshToken: tr.RefreshToken,
						ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
						Workspace:    workspace,
						Issuer:       issuer,
					}
					if err := authstore.Save(creds); err != nil {
						return err
					}
					if email := emailFromJWT(tr.AccessToken); email != "" {
						fmt.Printf("  %s %s\n\n", styleTitle.Render("logged in as"), email)
					} else {
						fmt.Printf("  %s\n\n", styleTitle.Render("logged in"))
					}
					return nil
				case "authorization_pending":
					continue
				case "slow_down":
					interval += 2 * time.Second
					continue
				default:
					return fmt.Errorf("login failed: %s (%s)", tr.Error, tr.ErrorDescription)
				}
			}
		},
	}
	c.Flags().StringVar(&issuer, "auth-url", defaultIssuer, "OIDC issuer (realm URL)")
	c.Flags().StringVar(&workspace, "workspace", "", "workspace slug to publish into (needed only with several workspaces)")
	return c
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored tinysystems.io session",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := authstore.Clear(); err != nil {
				return err
			}
			fmt.Println("  logged out")
			return nil
		},
	}
}

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who is logged in",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := authstore.Load()
			if err != nil {
				return err
			}
			email := emailFromJWT(creds.AccessToken)
			if email == "" {
				email = "(unknown)"
			}
			fmt.Printf("  %s", email)
			if creds.Workspace != "" {
				fmt.Printf(" · workspace %s", creds.Workspace)
			}
			fmt.Println()
			return nil
		},
	}
}

// freshAccessToken returns a valid access token from the store, refreshing
// through Keycloak when the stored one is stale.
func freshAccessToken(ctx context.Context) (*authstore.Credentials, error) {
	creds, err := authstore.Load()
	if err != nil {
		return nil, err
	}
	if time.Until(creds.ExpiresAt) > 30*time.Second {
		return creds, nil
	}
	httpc := &http.Client{Timeout: 30 * time.Second}
	issuer := creds.Issuer
	if issuer == "" {
		issuer = defaultIssuer
	}
	tr, err := postToken(httpc, issuer, url.Values{
		"client_id":     {oidcClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
	})
	if err != nil {
		return nil, err
	}
	if tr.Error != "" || tr.AccessToken == "" {
		return nil, fmt.Errorf("session expired — run `tiny login` again (%s)", tr.ErrorDescription)
	}
	creds.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		creds.RefreshToken = tr.RefreshToken
	}
	creds.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if err := authstore.Save(creds); err != nil {
		return nil, err
	}
	return creds, nil
}

func postToken(httpc *http.Client, issuer string, form url.Values) (*tokenResponse, error) {
	resp, err := httpc.PostForm(issuer+"/protocol/openid-connect/token", form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}
	return &tr, nil
}

// emailFromJWT extracts the email claim without verifying — display only;
// the server does the real verification.
func emailFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.Email
}

func openBrowser(u string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", u).Start()
	case "linux":
		_ = exec.Command("xdg-open", u).Start()
	}
}
