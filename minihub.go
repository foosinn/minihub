package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"encoding/json"
	"html/template"
	"io/ioutil"
	"net/http"

	_ "github.com/foosinn/minihub/statik"
	"github.com/rakyll/statik/fs"
)

var Config config

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
		Name         string `json:"Name"`
		Tag          string `json:"Tag"`
		Architecture string `json:"architecture"`
		History      []map[string]string
		FirstHistory registryInfo
	}
	registryInfo struct {
		Config struct {
			Env    []string
			Labels struct {
				Author     string `json:"io.openshift.s2i.build.commit.author"`
				CommitDate string `json:"io.openshift.s2i.build.commit.date"`
				Sha        string `json:"io.openshift.s2i.build.commit.id"`
				Ref        string `json:"io.openshift.s2i.build.commit.ref"`
				Repo       string `json:"io.openshift.s2i.build.source-location"`
				Message    string `json:"io.openshift.s2i.build.commit.message"`
				Image      string `json:"io.openshift.s2i.build.image"`
			}
		} `json:"config"`
	}

	templateTag struct {
		Name string
		Info registryInfo
	}
	templateImage struct {
		Name string
		Tags []templateTag
	}
	templateData struct {
		Registry string
		Images   []templateImage
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
	}
	indexTemplate := template.Must(template.New("index.html").Funcs(fm).Parse(string(indexContent)))

	Config = config{
		listen,
		registry,
		indexTemplate,
	}
}

func main() {
	log.Printf("Listening on %s...", Config.listen)
	http.HandleFunc("/", index)
	log.Fatal(http.ListenAndServe(Config.listen, nil))
}

func index(w http.ResponseWriter, r *http.Request) {
	resp, err := http.Get(fmt.Sprintf("https://%s/v2/_catalog", Config.registry))
	if err != nil {
		fmt.Fprintf(w, "Unable to get repositories.")
		log.Println(err)
		return
	}

	catalog := registryCatalog{}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		fmt.Fprintf(w, "Unable to parse JSON.")
		log.Println(err)
		return
	}

	images := []templateImage{}
	for _, imageName := range catalog.Repositories {
		resp, err := http.Get(fmt.Sprintf("https://%s/v2/%s/tags/list", Config.registry, imageName))
		if err != nil {
			fmt.Fprintf(w, "Unable to get tags.")
			log.Println(err)
			return
		}
		ri := registryImage{}
		if err := json.NewDecoder(resp.Body).Decode(&ri); err != nil {
			fmt.Fprintf(w, "Unable to parse JSON.")
			log.Println(err)
			return
		}

		image := templateImage{
			ri.Name,
			[]templateTag{},
		}
		for _, tagName := range ri.Tags {
			resp, err := http.Get(fmt.Sprintf("https://%s/v2/%s/manifests/%s", Config.registry, imageName, tagName))
			if err != nil {
				fmt.Fprintf(w, "Unable to load tag (%s) information.", tagName)
			}
			tagInfo := registryTag{}
			if err := json.NewDecoder(resp.Body).Decode(&tagInfo); err != nil {
				fmt.Fprintf(w, "Unable to parse tags (%s) json.", tagName)
				log.Println(err)
				return
			}
			json.Unmarshal([]byte(tagInfo.History[0]["v1Compatibility"]), &tagInfo.FirstHistory)

			tag := templateTag{tagName, tagInfo.FirstHistory}
			image.Tags = append(image.Tags, tag)
		}

		image.Tags = tagLimitSort(image.Tags)
		images = append(images, image)
	}

	data := templateData{
		Config.registry,
		images,
	}
	if err := Config.template.Execute(w, data); err != nil {
		fmt.Fprintf(w, "Unable to write template:\n %v", err)
		log.Println(err)
		return
	}
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
		iTime, _ := time.Parse("Mon Jan 02 15:04:05 2006 -0700", other[i].Info.Config.Labels.CommitDate)
		jTime, _ := time.Parse("Mon Jan 02 15:04:05 2006 -0700", other[j].Info.Config.Labels.CommitDate)
		return iTime.Unix() > jTime.Unix()
	})
	if len(other) > 4 {
		other = other[0:4]
	}
	return append(first, other...)
}
