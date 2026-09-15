package main

import (
	"encoding/json"
	"testing"
)

func TestYambyStudioDetails(t *testing.T) {
	a := testApp(t)
	_, err := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('studio-lib','test','/media/test','movies')")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,seen) VALUES('studio-movie','studio-lib','studio-lib','movie','Movie','/media/test/a.strm','http://example.invalid/a','g')")
	if err != nil {
		t.Fatal(err)
	}
	n := sidecar{Plot: "简介保留", Studios: []string{"iQiyi", "Youku", "Tencent Video", " "}}
	b, _ := json.Marshal(n)
	_, err = a.db.Exec("INSERT INTO item_metadata VALUES(?,?)", "studio-movie", string(b))
	if err != nil {
		t.Fatal(err)
	}
	x, err := a.item("studio-movie")
	if err != nil {
		t.Fatal(err)
	}
	m := a.dto(x)
	a.enrich(x, m)
	b, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Overview string
		Studios  []struct {
			Name string
			Id   *int64
		}
	}
	if err = json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	if result.Overview != n.Plot || len(result.Studios) != 3 {
		t.Fatal("metadata lost")
	}
	ids := map[int64]bool{}
	for _, studio := range result.Studios {
		if studio.Id == nil || *studio.Id <= 0 || ids[*studio.Id] {
			t.Fatal("missing or duplicate numeric studio ID")
		}
		ids[*studio.Id] = true
	}
	m2 := a.dto(x)
	a.enrich(x, m2)
	b1, _ := json.Marshal(m["Studios"])
	b2, _ := json.Marshal(m2["Studios"])
	if string(b1) != string(b2) {
		t.Fatal("unstable studio IDs")
	}
}
