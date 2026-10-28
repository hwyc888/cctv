package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/service"
	"cctv-sub/internal/store"
)

func TestSubscriptionEpisodeTableReturnsStatusesAndQueuesSelection(t *testing.T) {
	dir:=t.TempDir()
	st,err:=store.Open(context.Background(),filepath.Join(dir,"state.db"),filepath.Join(dir,"downloads"),model.Settings{PollIntervalMinutes:30,ConcurrentDownloads:2})
	if err!=nil {t.Fatal(err)}
	defer st.Close()
	category:=st.Snapshot().Categories[0]
	sub:=model.Subscription{ID:"sub-table-api",Title:"航拍中国",StorageCategoryID:category.ID,Status:"active",AutoDownload:true}
	episodes:=[]model.Episode{
		{ID:"ep-done",SubscriptionID:sub.ID,Number:1,Title:"第一集",PageURL:"https://tv.cctv.com/ep1.shtml"},
		{ID:"ep-failed",SubscriptionID:sub.ID,Number:2,Title:"第二集",PageURL:"https://tv.cctv.com/ep2.shtml"},
		{ID:"ep-missing",SubscriptionID:sub.ID,Number:3,Title:"第三集",PageURL:"https://tv.cctv.com/ep3.shtml"},
	}
	if err:=st.Update(func(s *model.State) error {
		s.Subscriptions=[]model.Subscription{sub};s.Episodes=episodes
		s.Downloads=[]model.DownloadTask{
			{ID:"task-done",SubscriptionID:sub.ID,EpisodeID:"ep-done",Title:"第一集",Status:"completed",FilePath:filepath.Join(dir,"done.mp4")},
			{ID:"task-failed",SubscriptionID:sub.ID,EpisodeID:"ep-failed",Title:"第二集",Status:"failed",Error:"网络错误"},
		}
		return nil
	});err!=nil {t.Fatal(err)}
	handler:=New(st,cctv.New(),service.NewManager(st,cctv.New()),slog.New(slog.NewTextHandler(io.Discard,nil)))
	response:=httptest.NewRecorder()
	handler.ServeHTTP(response,httptest.NewRequest(http.MethodGet,"/api/subscriptions/"+sub.ID+"/episodes",nil))
	if response.Code!=200 {t.Fatalf("list episodes: %d %s",response.Code,response.Body.String())}
	var list struct {Episodes []subscriptionEpisodeRow `json:"episodes"`; Total int `json:"total"`}
	if err:=json.Unmarshal(response.Body.Bytes(),&list);err!=nil {t.Fatal(err)}
	if list.Total!=3||len(list.Episodes)!=3||list.Episodes[0].Status!="completed"||list.Episodes[1].Status!="failed"||list.Episodes[2].Status!="not_downloaded" {t.Fatalf("incorrect episode status table: %#v",list)}
	body,_:=json.Marshal(map[string][]string{"episodeIds":{"ep-missing"}})
	response=httptest.NewRecorder()
	handler.ServeHTTP(response,httptest.NewRequest(http.MethodPost,"/api/subscriptions/"+sub.ID+"/episodes/queue",bytes.NewReader(body)))
	if response.Code!=200 {t.Fatalf("queue selected episode: %d %s",response.Code,response.Body.String())}
	if len(st.Snapshot().Downloads)!=3 {t.Fatalf("new episode task not created: %#v",st.Snapshot().Downloads)}
	badID:=httptest.NewRecorder()
	handler.ServeHTTP(badID,httptest.NewRequest(http.MethodPost,"/api/subscriptions/"+sub.ID+"/episodes/queue",bytes.NewReader([]byte(`{"episodeIds":["foreign"]}`))))
	if badID.Code!=http.StatusBadRequest {t.Fatalf("foreign episode accepted: %d",badID.Code)}
}
