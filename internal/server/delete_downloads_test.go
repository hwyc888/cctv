package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/service"
	"cctv-sub/internal/store"
)

func TestDeleteDownloadsHTTPRemovesRealFilesSingleAndBatch(t *testing.T) {
	dir:=t.TempDir()
	root:=filepath.Join(dir,"downloads")
	st,err:=store.Open(context.Background(),filepath.Join(dir,"state.db"),root,model.Settings{
		DownloadPath:root,PollIntervalMinutes:30,ConcurrentDownloads:2,
	})
	if err!=nil {t.Fatal(err)}
	defer st.Close()
	seriesDir:=filepath.Join(root,"测试动画")
	if err:=os.MkdirAll(seriesDir,0755);err!=nil {t.Fatal(err)}
	var paths []string
	for _,filename:=range []string{"one.mp4","two.mp4","three.mp4"} {
		path:=filepath.Join(seriesDir,filename)
		if err:=os.WriteFile(path,[]byte("MP4 fixture"),0644);err!=nil {t.Fatal(err)}
		paths=append(paths,path)
	}
	categoryID:=st.Snapshot().Categories[0].ID
	if err:=st.Update(func(s *model.State) error {
		s.Downloads=[]model.DownloadTask{
			{ID:"one",Status:"completed",FilePath:paths[0],StorageCategoryID:categoryID},
			{ID:"two",Status:"completed",FilePath:paths[1],StorageCategoryID:categoryID},
			{ID:"three",Status:"completed",FilePath:paths[2],StorageCategoryID:categoryID},
		}
		return nil
	});err!=nil {t.Fatal(err)}
	client:=cctv.New()
	server:=New(st,client,service.NewManager(st,client),slog.New(slog.NewTextHandler(io.Discard,nil)))
	one:=httptest.NewRecorder()
	server.ServeHTTP(one,httptest.NewRequest(http.MethodDelete,"/api/downloads/one",nil))
	if one.Code!=http.StatusOK {t.Fatalf("single delete failed: %d %s",one.Code,one.Body.String())}
	var count service.DeleteTasksResult
	if err:=json.Unmarshal(one.Body.Bytes(),&count);err!=nil || count.Deleted!=1 || count.FilesDeleted!=1 {
		t.Fatalf("single delete response: %#v %v",count,err)
	}
	if _,err:=os.Stat(paths[0]);!os.IsNotExist(err) {t.Fatal("single delete did not remove file",err)}
	body:=[]byte(`{"ids":["two","three"]}`)
	batch:=httptest.NewRecorder()
	server.ServeHTTP(batch,httptest.NewRequest(http.MethodPost,"/api/downloads/delete-batch",bytes.NewReader(body)))
	if batch.Code!=http.StatusOK {t.Fatalf("batch delete failed: %d %s",batch.Code,batch.Body.String())}
	if err:=json.Unmarshal(batch.Body.Bytes(),&count);err!=nil || count.Deleted!=2 || count.FilesDeleted!=2 || count.DirectoriesDeleted!=1 {
		t.Fatalf("batch delete response: %#v %v",count,err)
	}
	for _,filename:=range paths {
		if _,err:=os.Stat(filename);!os.IsNotExist(err) {t.Fatalf("file still present after API delete: %s %v",filename,err)}
	}
	if len(st.Snapshot().Downloads)!=0 {t.Fatal("download task metadata was not removed")}
	if _,err:=os.Stat(seriesDir);!os.IsNotExist(err) {t.Fatalf("empty series directory must be removed: %v",err)}
	if _,err:=os.Stat(root);err!=nil {t.Fatalf("configured download category root must stay: %v",err)}
}

func TestDeleteDownloadsHTTPRejectsBadIDsAndCrossSiteOrigin(t *testing.T) {
	dir:=t.TempDir()
	st,err:=store.Open(context.Background(),filepath.Join(dir,"state.db"),filepath.Join(dir,"downloads"),model.Settings{
		PollIntervalMinutes:30,ConcurrentDownloads:2,
	})
	if err!=nil {t.Fatal(err)}
	defer st.Close()
	client:=cctv.New()
	handler:=New(st,client,service.NewManager(st,client),slog.New(slog.NewTextHandler(io.Discard,nil)))
	for _,request:=range []*http.Request{
		httptest.NewRequest(http.MethodDelete,"/api/downloads/missing",nil),
		httptest.NewRequest(http.MethodPost,"/api/downloads/delete-batch",bytes.NewReader([]byte(`{"ids":[]}`))),
		httptest.NewRequest(http.MethodPost,"/api/downloads/delete-batch",bytes.NewReader([]byte(`{"ids":["missing"]}`))),
	} {
		out:=httptest.NewRecorder()
		handler.ServeHTTP(out,request)
		if out.Code!=http.StatusConflict {t.Fatalf("invalid deletion should be rejected: %d %s",out.Code,out.Body.String())}
	}
	crossSite:=httptest.NewRequest(http.MethodPost,"/api/downloads/delete-batch",bytes.NewReader([]byte(`{"ids":["some"]}`)))
	crossSite.Header.Set("Origin","https://evil.example")
	out:=httptest.NewRecorder()
	handler.ServeHTTP(out,crossSite)
	if out.Code!=http.StatusForbidden {t.Fatalf("cross-site deletion accepted: %d",out.Code)}
}
