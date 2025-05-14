// Copyright 2025 OpenPubkey
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"path/filepath"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/openpubkey/openpubkey/client"
	"github.com/openpubkey/openpubkey/client/choosers"
	"github.com/openpubkey/openpubkey/oidc"
	"github.com/openpubkey/openpubkey/pktoken"
	"github.com/openpubkey/openpubkey/providers"
	"github.com/openpubkey/openpubkey/util"
	"github.com/openpubkey/opkssh/commands/config"
	"github.com/openpubkey/opkssh/sshcert"
	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"
)

type LoginCmd struct {
	// Inputs
	Fs                    afero.Fs
	autoRefreshArg        bool
	configPathArg         string
	createConfigArg       bool
	logDirArg             string
	disableBrowserOpenArg bool
	printIdTokenArg       bool
	keyPathArg            string
	providerArg           string
	providerAliasArg      string
	verbosity             int                       // Default verbosity is 0, 1 is verbose, 2 is debug
	overrideProvider      *providers.OpenIdProvider // Used in tests to override the provider to inject a mock provider

	// State
	config *config.ClientConfig

	// Outputs
	pkt        *pktoken.PKToken
	signer     crypto.Signer
	alg        jwa.SignatureAlgorithm
	client     *client.OpkClient
	principals []string
}

func NewLogin(autoRefreshArg bool, configPathArg string, createConfigArg bool, logDirArg string, disableBrowserOpenArg bool, printIdTokenArg bool,
	providerArg string, keyPathArg string, providerAliasArg string) *LoginCmd {

	return &LoginCmd{
		Fs:                    afero.NewOsFs(),
		autoRefreshArg:        autoRefreshArg,
		configPathArg:         configPathArg,
		createConfigArg:       createConfigArg,
		logDirArg:             logDirArg,
		disableBrowserOpenArg: disableBrowserOpenArg,
		printIdTokenArg:       printIdTokenArg,
		keyPathArg:            keyPathArg,
		providerArg:           providerArg,
		providerAliasArg:      providerAliasArg,
	}
}

func (l *LoginCmd) Run(ctx context.Context) error {
	// If a log directory was provided, write any logs to a file in that directory AND stdout
	if l.logDirArg != "" {
		logFilePath := filepath.Join(l.logDirArg, "opkssh.log")
		logFile, err := l.Fs.OpenFile(logFilePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0660)
		if err != nil {
			log.Printf("Failed to open log for writing: %v \n", err)
		}
		defer logFile.Close()
		multiWriter := io.MultiWriter(os.Stdout, logFile)
		log.SetOutput(multiWriter)
	} else {
		log.SetOutput(os.Stdout)
	}

	if l.verbosity >= 2 {
		log.Printf("DEBUG: running login command with args: %+v", *l)
	}

	if l.configPathArg == "" {
		dir, dirErr := os.UserHomeDir()
		if dirErr != nil {
			return fmt.Errorf("failed to get user config dir: %w", dirErr)
		}
		l.configPathArg = filepath.Join(dir, ".opk", "config.yml")
	}

	if _, err := l.Fs.Stat(l.configPathArg); err == nil {
		if l.createConfigArg {
			log.Printf("--create-config=true but config file already exists at %s", l.configPathArg)
		}

		// Load the file from the filesystem
		if client_config, err := config.GetClientConfigFromFile((l.configPathArg), l.Fs); err != nil {
			return err
		} else {
			l.config = client_config
		}
	} else {
		if l.createConfigArg {
			return config.CreateDefaultClientConfig(l.configPathArg, l.Fs)
		} else {
			log.Printf("failed to find client config file to generate a default config, run `opkssh login --create-config` to create a default config file")
		}
		l.config, err = config.NewClientConfig(config.DefaultClientConfig)
		if err != nil {
			return fmt.Errorf("failed to parse default config file: %w", err)
		}
	}

	var provider providers.OpenIdProvider
	if l.overrideProvider != nil {
		provider = *l.overrideProvider
	} else {
		op, chooser, err := l.determineProvider()
		if err != nil {
			return err
		}
		if chooser != nil {
			provider, err = chooser.ChooseOp(ctx)
			if err != nil {
				return fmt.Errorf("error choosing provider: %w", err)
			}
		} else if op != nil {
			provider = op
		} else {
			return fmt.Errorf("no provider found") // Either the provider or the chooser must be set. If this occurs we have a bug in the code.
		}
	}

	// Execute login command
	if l.autoRefreshArg {
		if providerRefreshable, ok := provider.(providers.RefreshableOpenIdProvider); ok {
			err := l.LoginWithRefresh(ctx, providerRefreshable, l.printIdTokenArg, l.keyPathArg)
			if err != nil {
				return fmt.Errorf("error logging in: %w", err)
			}
		} else {
			return fmt.Errorf("supplied OpenID Provider (%v) does not support auto-refresh and auto-refresh argument set to true", provider.Issuer())
		}
	} else {
		err := l.Login(ctx, provider, l.printIdTokenArg, l.keyPathArg)
		if err != nil {
			return fmt.Errorf("error logging in: %w", err)
		}
	}
	return nil
}

func (l *LoginCmd) determineProvider() (providers.OpenIdProvider, *choosers.WebChooser, error) {
	openBrowser := !l.disableBrowserOpenArg

	var defaultProviderAlias string
	var providerConfigs []config.ProviderConfig
	var provider providers.OpenIdProvider
	var err error

	// If the user has supplied commandline arguments for the provider, short circuit and use providerArg
	if l.providerArg != "" {
		providerConfig, err := config.NewProviderConfigFromString(l.providerArg, false)
		if err != nil {
			return nil, nil, fmt.Errorf("error parsing provider argument: %w", err)
		}

		if provider, err = providerConfig.ToProvider(openBrowser); err != nil {
			return nil, nil, fmt.Errorf("error creating provider from config: %w", err)
		} else {
			return provider, nil, nil
		}
	}

	// Set the default provider from the env variable if specified
	defaultProviderEnv, _ := os.LookupEnv(config.OPKSSH_DEFAULT_ENVVAR)
	providerConfigsEnv, err := config.GetProvidersConfigFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting provider config from env: %w", err)
	}

	if l.providerAliasArg != "" {
		defaultProviderAlias = l.providerAliasArg
	} else if defaultProviderEnv != "" {
		defaultProviderAlias = defaultProviderEnv
	} else if l.config.DefaultProvider != "" {
		defaultProviderAlias = l.config.DefaultProvider
	} else {
		defaultProviderAlias = config.WEBCHOOSER_ALIAS
	}

	if providerConfigsEnv != nil {
		providerConfigs = providerConfigsEnv
	} else if len(l.config.Providers) > 0 {
		providerConfigs = l.config.Providers
	} else {
		return nil, nil, fmt.Errorf("no providers specified")
	}

	if strings.ToUpper(defaultProviderAlias) != config.WEBCHOOSER_ALIAS {
		providerMap, err := config.CreateProvidersMap(providerConfigs)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating provider map: %w", err)
		}
		providerConfig, ok := providerMap[defaultProviderAlias]
		if !ok {
			return nil, nil, fmt.Errorf("error getting provider config for alias %s", defaultProviderAlias)
		}
		provider, err = providerConfig.ToProvider(openBrowser)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating provider from config: %w", err)
		}
		return provider, nil, nil
	} else {
		// If the default provider is WEBCHOOSER, we need to create a chooser and return it
		var providerList []providers.BrowserOpenIdProvider
		for _, providerConfig := range providerConfigs {
			op, err := providerConfig.ToProvider(openBrowser)
			if err != nil {
				return nil, nil, fmt.Errorf("error creating provider from config: %w", err)
			}
			providerList = append(providerList, op.(providers.BrowserOpenIdProvider))
		}

		chooser := choosers.NewWebChooser(
			providerList, openBrowser,
		)
		return nil, chooser, nil
	}
}

func (l *LoginCmd) login(ctx context.Context, provider providers.OpenIdProvider, printIdToken bool, seckeyPath string) (*LoginCmd, error) {
	var err error
	alg := jwa.ES256
	signer, err := util.GenKeyPair(alg)
	if err != nil {
		return nil, fmt.Errorf("failed to generate keypair: %w", err)
	}

	opkClient, err := client.New(provider, client.WithSigner(signer, alg))
	if err != nil {
		return nil, err
	}

	pkt, err := opkClient.Auth(ctx)
	if err != nil {
		return nil, err
	}

	// If principals is empty the server does not enforce any principal. The OPK
	// verifier should use policy to make this decision.
	principals := []string{}
	certBytes, seckeySshPem, err := createSSHCert(pkt, signer, principals)
	if err != nil {
		return nil, fmt.Errorf("failed to generate SSH cert: %w", err)
	}

	// Write ssh secret key and public key to filesystem
	if seckeyPath != "" {
		// If we have set seckeyPath then write it there
		if err := l.writeKeys(seckeyPath, seckeyPath+".pub", seckeySshPem, certBytes); err != nil {
			return nil, fmt.Errorf("failed to write SSH keys to filesystem: %w", err)
		}
	} else {
		// If keyPath isn't set then write it to the default location
		if err := l.writeKeysToSSHDir(seckeySshPem, certBytes); err != nil {
			return nil, fmt.Errorf("failed to write SSH keys to filesystem: %w", err)
		}
	}

	if printIdToken {
		idTokenStr, err := PrettyIdToken(*pkt)

		if err != nil {
			return nil, fmt.Errorf("failed to format ID Token: %w", err)
		}

		fmt.Printf("id_token:\n%s\n", idTokenStr)
	}

	idStr, err := IdentityString(*pkt)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ID Token: %w", err)
	}
	fmt.Printf("Keys generated for identity\n%s\n", idStr)

	return &LoginCmd{
		pkt:        pkt,
		signer:     signer,
		client:     opkClient,
		alg:        alg,
		principals: principals,
	}, nil
}

// Login performs the OIDC login procedure and creates the SSH certs/keys in the
// default SSH key location.
func (l *LoginCmd) Login(ctx context.Context, provider providers.OpenIdProvider, printIdToken bool, seckeyPath string) error {
	_, err := l.login(ctx, provider, printIdToken, seckeyPath)
	return err
}

// LoginWithRefresh performs the OIDC login procedure, creates the SSH
// certs/keys in the default SSH key location, and continues to run and refresh
// the PKT (and create new SSH certs) indefinitely as its token expires. This
// function only returns if it encounters an error or if the supplied context is
// cancelled.
func (l *LoginCmd) LoginWithRefresh(ctx context.Context, provider providers.RefreshableOpenIdProvider, printIdToken bool, seckeyPath string) error {
	if loginResult, err := l.login(ctx, provider, printIdToken, seckeyPath); err != nil {
		return err
	} else {
		var claims struct {
			Expiration int64 `json:"exp"`
		}
		if err := json.Unmarshal(loginResult.pkt.Payload, &claims); err != nil {
			return err
		}

		for {
			// Sleep until a minute before expiration to give us time to refresh
			// the token and minimize any interruptions
			untilExpired := time.Until(time.Unix(claims.Expiration, 0)) - time.Minute
			log.Printf("Waiting for %v before attempting to refresh id_token...", untilExpired)
			select {
			case <-time.After(untilExpired):
				log.Print("Refreshing id_token...")
			case <-ctx.Done():
				return ctx.Err()
			}

			refreshedPkt, err := loginResult.client.Refresh(ctx)
			if err != nil {
				return err
			}
			loginResult.pkt = refreshedPkt

			certBytes, seckeySshPem, err := createSSHCert(loginResult.pkt, loginResult.signer, loginResult.principals)
			if err != nil {
				return fmt.Errorf("failed to generate SSH cert: %w", err)
			}

			// Write ssh secret key and public key to filesystem
			if seckeyPath != "" {
				// If we have set seckeyPath then write it there
				if err := l.writeKeys(seckeyPath, seckeyPath+".pub", seckeySshPem, certBytes); err != nil {
					return fmt.Errorf("failed to write SSH keys to filesystem: %w", err)
				}
			} else {
				// If keyPath isn't set then write it to the default location
				if err := l.writeKeysToSSHDir(seckeySshPem, certBytes); err != nil {
					return fmt.Errorf("failed to write SSH keys to filesystem: %w", err)
				}
			}

			comPkt, err := refreshedPkt.Compact()
			if err != nil {
				return err
			}

			_, payloadB64, _, err := jws.SplitCompactString(string(comPkt))
			if err != nil {
				return fmt.Errorf("malformed ID token: %w", err)
			}
			payload, err := base64.RawURLEncoding.DecodeString(string(payloadB64))
			if err != nil {
				return fmt.Errorf("refreshed ID token payload is not base64 encoded: %w", err)
			}

			if err = json.Unmarshal(payload, &claims); err != nil {
				return fmt.Errorf("malformed refreshed ID token payload: %w", err)
			}
		}
	}
}

func createSSHCert(pkt *pktoken.PKToken, signer crypto.Signer, principals []string) ([]byte, []byte, error) {
	cert, err := sshcert.New(pkt, principals)
	if err != nil {
		return nil, nil, err
	}
	sshSigner, err := ssh.NewSignerFromSigner(signer)
	if err != nil {
		return nil, nil, err
	}

	signerMas, err := ssh.NewSignerWithAlgorithms(sshSigner.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoECDSA256})
	if err != nil {
		return nil, nil, err
	}

	sshCert, err := cert.SignCert(signerMas)
	if err != nil {
		return nil, nil, err
	}
	certBytes := ssh.MarshalAuthorizedKey(sshCert)
	// Remove newline character that MarshalAuthorizedKey() adds
	certBytes = certBytes[:len(certBytes)-1]

	seckeySsh, err := ssh.MarshalPrivateKey(signer, "openpubkey cert")
	if err != nil {
		return nil, nil, err
	}
	seckeySshBytes := pem.EncodeToMemory(seckeySsh)

	return certBytes, seckeySshBytes, nil
}

func (l *LoginCmd) writeKeysToSSHDir(seckeySshPem []byte, certBytes []byte) error {
	homePath, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	sshPath := filepath.Join(homePath, ".ssh")

	// Make ~/.ssh if folder does not exist
	err = l.Fs.MkdirAll(sshPath, os.ModePerm)
	if err != nil {
		return err
	}

	// For ssh to automatically find the key created by openpubkey when
	// connecting, we use one of the default ssh key paths. However, the file
	// might contain an existing key. We will overwrite the key if it was
	// generated by openpubkey  which we check by looking at the associated
	// comment. If the comment is equal to "openpubkey", we overwrite the file
	// with a new key.
	for _, keyFilename := range []string{"id_ecdsa", "id_ed25519"} {
		seckeyPath := filepath.Join(sshPath, keyFilename)
		pubkeyPath := seckeyPath + ".pub"

		if !l.fileExists(seckeyPath) {
			// If ssh key file does not currently exist, we don't have to worry about overwriting it
			return l.writeKeys(seckeyPath, pubkeyPath, seckeySshPem, certBytes)
		} else if !l.fileExists(pubkeyPath) {
			continue
		} else {
			// If the ssh key file does exist, check if it was generated by openpubkey, if it was then it is safe to overwrite
			afs := &afero.Afero{Fs: l.Fs}
			sshPubkey, err := afs.ReadFile(pubkeyPath)
			if err != nil {
				log.Println("Failed to read:", pubkeyPath)
				continue
			}
			_, comment, _, _, err := ssh.ParseAuthorizedKey(sshPubkey)
			if err != nil {
				log.Println("Failed to parse:", pubkeyPath)
				continue
			}

			// If the key comment is "openpubkey" then we generated it
			if comment == "openpubkey" {
				return l.writeKeys(seckeyPath, pubkeyPath, seckeySshPem, certBytes)
			}
		}
	}
	return fmt.Errorf("no default ssh key file free for openpubkey")
}

func (l *LoginCmd) writeKeys(seckeyPath string, pubkeyPath string, seckeySshPem []byte, certBytes []byte) error {
	// Write ssh secret key to filesystem
	afs := &afero.Afero{Fs: l.Fs}
	if err := afs.WriteFile(seckeyPath, seckeySshPem, 0600); err != nil {
		return err
	}

	fmt.Printf("Writing opk ssh public key to %s and corresponding secret key to %s\n", pubkeyPath, seckeyPath)

	certBytes = append(certBytes, []byte(" openpubkey")...)
	// Write ssh public key (certificate) to filesystem
	return afs.WriteFile(pubkeyPath, certBytes, 0644)
}

func (l *LoginCmd) fileExists(fPath string) bool {
	_, err := l.Fs.Open(fPath)
	return !errors.Is(err, os.ErrNotExist)
}

func IdentityString(pkt pktoken.PKToken) (string, error) {
	idt, err := oidc.NewJwt(pkt.OpToken)
	if err != nil {
		return "", err
	}
	claims := idt.GetClaims()
	if claims.Email == "" {
		return "Sub, issuer, audience: \n" + claims.Subject + " " + claims.Issuer + " " + claims.Audience, nil
	} else {
		return "Email, sub, issuer, audience: \n" + claims.Email + " " + claims.Subject + " " + claims.Issuer + " " + claims.Audience, nil
	}
}

func PrettyIdToken(pkt pktoken.PKToken) (string, error) {
	idt, err := oidc.NewJwt(pkt.OpToken)
	if err != nil {
		return "", err
	}
	idtJson, err := json.MarshalIndent(idt.GetClaims(), "", "    ")

	if err != nil {
		return "", err
	}
	return string(idtJson[:]), nil
}
