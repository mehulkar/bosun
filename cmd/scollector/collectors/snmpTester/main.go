package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"

	"bosun.org/_third_party/github.com/BurntSushi/toml"
	"bosun.org/cmd/scollector/collectors"
	"bosun.org/cmd/scollector/conf"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "index.html") })
	http.HandleFunc("/test", TestMib)
	http.HandleFunc("/toml", Toml)
	http.ListenAndServe(":8888", nil)
}

func TestMib(w http.ResponseWriter, r *http.Request) {
	buf, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	mib := conf.MIB{}
	err = json.Unmarshal(buf, &mib)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	snmp := conf.SNMP{}
	err = json.Unmarshal(buf, &snmp)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	md, err := collectors.GenericSnmp(snmp, mib)
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), 500)
		return
	}

	mdJson, err := json.MarshalIndent(md, "", "  ")
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write(mdJson)
}

func Toml(w http.ResponseWriter, r *http.Request) {
	buf, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	mib := conf.MIB{}
	err = json.Unmarshal(buf, &mib)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	meta := &struct{ Name string }{}
	err = json.Unmarshal(buf, meta)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	toToml := struct {
		MIBs map[string]conf.MIB
	}{MIBs: map[string]conf.MIB{meta.Name: mib}}

	toml.NewEncoder(w).Encode(toToml)
}
