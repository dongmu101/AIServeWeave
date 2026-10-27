package comfyeditor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"strings"
)

type localizationManifest struct {
	SchemaVersion       int               `json:"schema_version"`
	FrontendVersion     string            `json:"frontend_version"`
	SourceCommit        string            `json:"source_commit"`
	SourceArchiveSHA256 string            `json:"source_archive_sha256"`
	ConfigurationSHA256 string            `json:"configuration_sha256"`
	Locales             map[string]string `json:"locales"`
	Toolchain           map[string]string `json:"toolchain"`
	Patches             map[string]string `json:"patches"`
	Resources           map[string]string `json:"resources"`
	Files               map[string]string `json:"files"`
}

func validateLocalization(dist fs.FS) error {
	file, err := dist.Open("aisw-i18n-manifest.json")
	if err != nil {
		return errors.New("comfyeditor: localized assets require aisw-i18n-manifest.json; rebuild with fetch-comfyui-frontend.sh --localized")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > 2<<20 {
		return errors.New("comfyeditor: localization manifest exceeds size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 2<<20))
	decoder.DisallowUnknownFields()
	var manifest localizationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("comfyeditor: invalid localization manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("comfyeditor: trailing localization manifest data")
	}
	_, catalog := Catalog()
	if manifest.SchemaVersion != 1 || manifest.FrontendVersion != catalog.FrontendVersion ||
		manifest.SourceCommit != localizedSourceCommit || manifest.SourceArchiveSHA256 != localizedSourceArchiveSHA256 ||
		manifest.ConfigurationSHA256 != localizedConfigurationSHA256 || !maps.Equal(manifest.Locales, localizedLocales) {
		return errors.New("comfyeditor: localization version mismatch; install assets built for this control plane")
	}
	if len(manifest.Files) == 0 || manifest.Files["index.html"] == "" {
		return errors.New("comfyeditor: localization manifest has no editor index")
	}
	for name, digest := range manifest.Files {
		if !fs.ValidPath(name) || name == "aisw-i18n-manifest.json" || strings.HasSuffix(name, ".map") || len(digest) != sha256.Size*2 {
			return errors.New("comfyeditor: invalid artifact manifest entry")
		}
		f, err := dist.Open(name)
		if err != nil {
			return fmt.Errorf("comfyeditor: missing localized artifact %q", name)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != digest {
			return fmt.Errorf("comfyeditor: localized artifact digest mismatch %q", name)
		}
	}
	return fs.WalkDir(dist, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.New("comfyeditor: localized artifacts cannot contain symlinks")
		}
		if !entry.IsDir() && name != "aisw-i18n-manifest.json" && manifest.Files[name] == "" {
			return fmt.Errorf("comfyeditor: unmanifested localized artifact %q", name)
		}
		return nil
	})
}
