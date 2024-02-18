package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"

	"github.com/Masterminds/semver/v3"
	"github.com/gorilla/mux"
)

func New() http.Handler {
	router := mux.NewRouter()
	router.Handle("/package/{package}/{version}", http.HandlerFunc(packageHandler))
	return router
}

type npmPackageMetaResponse struct {
	Versions map[string]npmPackageResponse `json:"versions"`
}

type npmPackageResponse struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
}

type NpmPackageVersion struct {
	Name         string                        `json:"name"`
	Version      string                        `json:"version"`
	Dependencies map[string]*NpmPackageVersion `json:"dependencies"`
}

var PackageCache []*NpmPackageVersion
var mtx sync.Mutex

func GetPackageFromCache(name string, version string) *NpmPackageVersion {
	mtx.Lock()
	defer mtx.Unlock()

	for i := range PackageCache {
		if PackageCache[i].Name == name && PackageCache[i].Version == version {
			log.Printf("found in cache! name: %v, version: %v \n", name, version)
			return PackageCache[i]
		}
	}

	return nil
}

func AddPackageToCache(pkg *NpmPackageVersion) {
	mtx.Lock()
	defer mtx.Unlock()
	PackageCache = append(PackageCache, pkg)
	log.Printf("putting object to cache. name: %v, version: %v \n", pkg.Name, pkg.Version)
}

func packageHandler(w http.ResponseWriter, r *http.Request) {
	var wg sync.WaitGroup
	vars := mux.Vars(r)
	pkgName := vars["package"]
	pkgVersion := vars["version"]

	cachepkg := GetPackageFromCache(pkgName, pkgVersion)
	if cachepkg == nil {
		wg.Add(1)
		rootPkg := &NpmPackageVersion{Name: pkgName, Dependencies: map[string]*NpmPackageVersion{}}

		resolveDependencies(rootPkg, pkgVersion, &wg)
		wg.Wait()

		AddPackageToCache(rootPkg)

		stringified, err := json.MarshalIndent(rootPkg, "", "  ")
		if err != nil {
			println(err.Error())
			w.WriteHeader(500)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)

		// Ignoring ResponseWriter errors
		_, _ = w.Write(stringified)

	} else {
		stringified, err := json.MarshalIndent(cachepkg, "", "  ")
		if err != nil {
			println(err.Error())
			w.WriteHeader(500)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)

		// Ignoring ResponseWriter errors
		_, _ = w.Write(stringified)
	}

}

func resolveDependencies(pkg *NpmPackageVersion, versionConstraint string, wg *sync.WaitGroup) error {
	defer wg.Done()
	pkgMeta, err := fetchPackageMeta(pkg.Name)
	if err != nil {
		return err
	}
	concreteVersion, err := highestCompatibleVersion(versionConstraint, pkgMeta)
	if err != nil {
		return err
	}
	pkg.Version = concreteVersion

	npmPkg, err := fetchPackage(pkg.Name, pkg.Version)
	if err != nil {
		return err
	}
	for dependencyName, dependencyVersionConstraint := range npmPkg.Dependencies {
		wg.Add(1)
		dep := &NpmPackageVersion{Name: dependencyName, Dependencies: map[string]*NpmPackageVersion{}}
		pkg.Dependencies[dependencyName] = dep
		go resolveDependencies(dep, dependencyVersionConstraint, wg)
	}
	return nil
}

func highestCompatibleVersion(constraintStr string, versions *npmPackageMetaResponse) (string, error) {
	constraint, err := semver.NewConstraint(constraintStr)
	if err != nil {
		return "", err
	}
	filtered := filterCompatibleVersions(constraint, versions)
	sort.Sort(filtered)
	if len(filtered) == 0 {
		return "", errors.New("no compatible versions found")
	}
	return filtered[len(filtered)-1].String(), nil
}

func filterCompatibleVersions(constraint *semver.Constraints, pkgMeta *npmPackageMetaResponse) semver.Collection {
	var compatible semver.Collection
	for version := range pkgMeta.Versions {
		semVer, err := semver.NewVersion(version)
		if err != nil {
			continue
		}
		if constraint.Check(semVer) {
			compatible = append(compatible, semVer)
		}
	}
	return compatible
}

func fetchPackage(name, version string) (*npmPackageResponse, error) {
	resp, err := http.Get(fmt.Sprintf("https://registry.npmjs.org/%s/%s", name, version))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed npmPackageResponse
	_ = json.Unmarshal(body, &parsed)
	return &parsed, nil
}

func fetchPackageMeta(p string) (*npmPackageMetaResponse, error) {
	resp, err := http.Get(fmt.Sprintf("https://registry.npmjs.org/%s", p))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed npmPackageMetaResponse
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, err
	}

	return &parsed, nil
}
