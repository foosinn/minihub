package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"encoding/json"
	"html/template"
	"io/ioutil"
	"net/http"

	_ "github.com/foosinn/minihub/statik"
	"github.com/rakyll/statik/fs"
)

var c config

type (
	config struct {
		listen   string
		registry string
		template *template.Template
	}

	registryCatalog struct {
		Repositories []string `json:"repositories"`
	}
	registryImage struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	registryTag struct {
		Name                string `json:"Name"`
		Tag                 string `json:"Tag"`
		Architecture        string `json:"architecture"`
		DockerContentDigest string
		History             []map[string]string
		FirstHistory        registryInfo
	}
	registryInfo struct {
		Config struct {
			//lint:ignore U1000 Json decode is not detected
			Env    []string
			Labels struct {
				CommitDate string `json:"io.openshift.s2i.build.commit.date"`
				//lint:ignore U1000 Json decode is not detected
				Sha string `json:"io.openshift.s2i.build.commit.id"`
				//lint:ignore U1000 Json decode is not detected
				Ref string `json:"io.openshift.s2i.build.commit.ref"`
				//lint:ignore U1000 Json decode is not detected
				Repo string `json:"io.openshift.s2i.build.source-location"`
				//lint:ignore U1000 Json decode is not detected
				Message string `json:"io.openshift.s2i.build.commit.message"`
				//lint:ignore U1000 Json decode is not detected
				Image string `json:"io.openshift.s2i.build.image"`
			}
		} `json:"config"`
	}

	templateTag struct {
		Name                string
		Info                registryInfo
		DockerContentDigest string
	}
	templateImage struct {
		Name string
		Tags []templateTag
	}
	templateData struct {
		Registry string
		Images   []templateImage
		Messages []msg
	}

	msg struct {
		Level   string
		Message string
	}
)

func init() {
	listen, ok := os.LookupEnv("LISTEN")
	if !ok {
		listen = ":8080"
	}
	registry, ok := os.LookupEnv("REGISTRY")
	if !ok {
		registry = "registry.local"
	}

	statikFs, _ := fs.New()
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(statikFs)))
	indexFile, _ := statikFs.Open("/index.html")
	indexContent, _ := ioutil.ReadAll(indexFile)

	fm := template.FuncMap{
		"replace": func(old string, new string, input string) string {
			result := strings.ReplaceAll(input, old, new)
			return result
		},
		"get": func(field string, input []interface{}) string {
			for _, i := range input {
				i := i.(string)
				prefix := fmt.Sprintf("%s=", field)
				if strings.HasPrefix(i, prefix) {
					return strings.Replace(i, prefix, "", 1)
				}
			}
			return ""
		},
		"json": func(v interface{}) string {
			s, _ := json.MarshalIndent(v, "", "  ")
			return string(s)
		},
		"prefix": func(s string, prefix string) bool {
			return strings.HasPrefix(s, prefix)
		},
	}
	indexTemplate := template.Must(template.New("index.html").Funcs(fm).Parse(string(indexContent)))

	c = config{
		listen,
		registry,
		indexTemplate,
	}
}

func main() {
	log.Printf("Listening on %s...", c.listen)
	http.HandleFunc("/", index)
	http.HandleFunc("/delete", deleteTag)
	http.HandleFunc("/favicon.ico", favicon)
	log.Fatal(http.ListenAndServe(c.listen, nil))
}

func favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=7776000")
	fmt.Fprint(w, "data:image/x-icon;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQEAYAAABPYyMiAAAABmJLR0T///////8JWPfcAAAACXBIWXMAAABIAAAASABGyWs+AAAAF0lEQVRIx2NgGAWjYBSMglEwCkbBSAcACBAAAeaR9cIAAAAASUVORK5CYII=\n")
}

func index(w http.ResponseWriter, r *http.Request) {
	errs := make(chan error)
	errHandlerDone := sync.WaitGroup{}
	templateImages := []templateImage{}

	msgList := []msg{}
	errHandlerDone.Add(1)
	go func() {
		for err := range errs {
			log.Println(err)
			msgList = append(msgList, msg{"danger", fmt.Sprintf("%v", err)})
		}
		errHandlerDone.Done()
	}()

	defer func() {
		close(errs)
		errHandlerDone.Wait()
		data := templateData{c.registry, templateImages, msgList}
		if err := c.template.Execute(w, data); err != nil {
			log.Println(err)
		}
	}()

	// fetch catalog
	resp, err := http.Get(fmt.Sprintf("https://%s/v2/_catalog", c.registry))
	if err != nil {
		errs <- err
		return
	}
	catalog := registryCatalog{}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		errs <- err
		return
	}

	// fetch images
	images := make(chan templateImage)
	imageWait := sync.WaitGroup{}
	for ri := range fetchRegistryImages(catalog.Repositories, errs) {
		imageWait.Add(1)
		go func(ri registryImage) {
			image := templateImage{ri.Name, []templateTag{}}
			for tagInfo := range fetchRegistryTags(ri, errs) {
				tag := templateTag{tagInfo.Name, tagInfo.FirstHistory, tagInfo.DockerContentDigest}
				image.Tags = append(image.Tags, tag)
			}
			image.Tags = tagLimitSort(image.Tags)
			images <- image
			imageWait.Done()
		}(ri)
	}
	go func() {
		imageWait.Wait()
		close(images)
	}()
	for image := range images {
		templateImages = append(templateImages, image)
	}

}

func fetchRegistryImages(images []string, errs chan error) chan registryImage {
	out := make(chan registryImage)
	go func() {
		defer close(out)
		for _, imageName := range images {
			resp, err := http.Get(fmt.Sprintf("https://%s/v2/%s/tags/list", c.registry, imageName))
			if err != nil {
				errs <- err
				return
			}
			ri := registryImage{}
			if err := json.NewDecoder(resp.Body).Decode(&ri); err != nil {
				errs <- err
				return
			}
			out <- ri
		}
	}()
	return out
}

func fetchRegistryTags(ri registryImage, errs chan error) chan registryTag {
	out := make(chan registryTag)
	go func() {
		defer close(out)
		for _, tagName := range ri.Tags {
			// get tag info
			url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", c.registry, ri.Name, tagName)
			resp, err := http.Get(url)
			if err != nil {
				errs <- err
				return
			}
			ti := registryTag{}
			if err := json.NewDecoder(resp.Body).Decode(&ti); err != nil {
				errs <- err
				return
			}
			json.Unmarshal([]byte(ti.History[0]["v1Compatibility"]), &ti.FirstHistory)

			// get delete token
			client := &http.Client{}
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				errs <- err
				return
			}
			req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
			resp, err = client.Do(req)
			if err != nil {
				errs <- err
				return
			}
			ti.DockerContentDigest = resp.Header["Docker-Content-Digest"][0]
			ti.Name = tagName

			out <- ti
		}
	}()
	return out
}

func deleteTag(w http.ResponseWriter, r *http.Request) {
	defer http.Redirect(w, r, "/", http.StatusFound)

	_ = r.ParseForm()
	image := r.Form.Get("Image")
	digest := r.Form.Get("DockerContentDigest")
	if image == "" || digest == "" {
		log.Printf("No image to delete '%s/%s'.", image, digest)
		return
	}
	log.Printf("Deleting image %s/%s.", image, digest)
	req, err := http.NewRequest("DELETE", fmt.Sprintf("https://%s/v2/%s/manifests/%s", c.registry, image, digest), nil)
	if err != nil {
		log.Printf("Error while deleting image.")
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error while deleting image.")
	}
	log.Printf("resp: %v", resp)

}

func tagLimitSort(tags []templateTag) []templateTag {
	first := []templateTag{}
	other := []templateTag{}
	for _, t := range tags {
		if t.Name == "latest" {
			first = append(first, t)
		} else {
			other = append(other, t)
		}
	}
	sort.Slice(other, func(i int, j int) bool {
		iTime, _ := time.Parse("Mon Jan 2 15:04:05 2006 -0700", other[i].Info.Config.Labels.CommitDate)
		jTime, _ := time.Parse("Mon Jan 2 15:04:05 2006 -0700", other[j].Info.Config.Labels.CommitDate)
		return iTime.Unix() > jTime.Unix()
	})
	if len(other) > 4 {
		other = other[0:4]
	}
	return append(first, other...)
}
