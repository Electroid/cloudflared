package transfer

import (
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	homedir "github.com/mitchellh/go-homedir"
)

const (
	baseLoginURL          = "https://dash.cloudflare.com/argotunnel"
	callbackStoreURL      = "https://login.argotunnel.com/"
	defaultCredentialFile = "cert.pem"
)

var (
	// File names from which we attempt to read configuration.
	defaultConfigFiles = []string{"config.yml", "config.yaml"}

	// Launchd doesn't set root env variables, so there is default
	// Windows default config dir was ~/cloudflare-warp in documentation; let's keep it compatible
	defaultConfigDirs = []string{"~/.cloudflared", "~/.cloudflare-warp", "~/cloudflare-warp", "/usr/local/etc/cloudflared", "/etc/cloudflared"}
)

func Login() error {
	path, ok, err := checkForExistingCert()
	if ok {
		fmt.Fprintf(os.Stdout, "You have an existing certificate at %s which login would overwrite.\nIf this is intentional, please move or delete that file then run this command again.\n", path)
		return nil
	} else if err != nil {
		return err
	}

	loginURL, err := url.Parse(baseLoginURL)
	if err != nil {
		// shouldn't happen, URL is hardcoded
		return err
	}

	_, err = run(loginURL, "cert", "callback", callbackStoreURL, path, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write the certificate due to the following error:\n%v\n\nYour browser will download the certificate instead. You will have to manually\ncopy it to the following path:\n\n%s\n", err, path)
		return err
	}

	fmt.Fprintf(os.Stdout, "You have successfully logged in.\nIf you wish to copy your credentials to a server, they have been saved to:\n%s\n", path)
	return nil
}

func Cert() ([]byte, error) {
	path, _, err := checkForExistingCert()
	if err != nil {
		return nil, err
	}

	return ioutil.ReadFile(path)
}

func checkForExistingCert() (string, bool, error) {
	configPath, err := homedir.Expand(defaultConfigDirs[0])
	if err != nil {
		return "", false, err
	}
	ok, err := fileExists(configPath)
	if !ok && err == nil {
		// create config directory if doesn't already exist
		err = os.Mkdir(configPath, 0700)
	}
	if err != nil {
		return "", false, err
	}
	path := filepath.Join(configPath, defaultCredentialFile)
	fileInfo, err := os.Stat(path)
	if err == nil && fileInfo.Size() > 0 {
		return path, true, nil
	}
	if err != nil && err.(*os.PathError).Err != syscall.ENOENT {
		return path, false, err
	}

	return path, false, nil
}

// fileExists checks to see if a file exist at the provided path.
func fileExists(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// ignore missing files
			return false, nil
		}
		return false, err
	}
	f.Close()
	return true, nil
}

// findDefaultConfigPath returns the first path that contains a config file.
// If none of the combination of DefaultConfigDirs and DefaultConfigFiles
// contains a config file, return empty string.
func findDefaultConfigPath() string {
	for _, configDir := range defaultConfigDirs {
		for _, configFile := range defaultConfigFiles {
			dirPath, err := homedir.Expand(configDir)
			if err != nil {
				continue
			}
			path := filepath.Join(dirPath, configFile)
			if ok, _ := fileExists(path); ok {
				return path
			}
		}
	}
	return ""
}
