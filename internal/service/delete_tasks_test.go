package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/store"
)

func setupTaskDeleteTest(t *testing.T) (*Manager, *store.Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "downloads")
	st, err := store.Open(context.Background(), filepath.Join(dir, "state.db"), root, model.Settings{
		DownloadPath: root, PollIntervalMinutes: 30, ConcurrentDownloads: 2,
	})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = st.Close() })
	return NewManager(st, cctv.New()), st, dir, st.Snapshot().Categories[0].ID
}

func createTaskDeleteFile(t *testing.T, base, name string) string {
	t.Helper()
	path := filepath.Join(base, "动画节目", name+".mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil { t.Fatal(err) }
	if err := os.WriteFile(path, []byte("real video data"), 0644); err != nil { t.Fatal(err) }
	return path
}

func TestDeleteTasksActuallyRemovesFilesAndKeepsUnselectedFiles(t *testing.T) {
	m, st, dir, categoryID := setupTaskDeleteTest(t)
	root := filepath.Join(dir, "downloads")
	one := createTaskDeleteFile(t, root, "first")
	two := createTaskDeleteFile(t, root, "second")
	keep := createTaskDeleteFile(t, root, "keep")
	if err := st.Update(func(s *model.State) error {
		s.Subscriptions = append(s.Subscriptions, model.Subscription{ID:"sub", Title:"动画节目", StorageCategoryID:categoryID, DownloadedCount:1})
		s.Downloads = []model.DownloadTask{
			{ID:"one", Status:"completed", FilePath:one, Title:"first", SubscriptionID:"sub", EpisodeID:"e1"},
			{ID:"two", Status:"completed", FilePath:two, Title:"second", StorageCategoryID:categoryID},
			{ID:"keep", Status:"completed", FilePath:keep, Title:"keep", StorageCategoryID:categoryID},
		}
		return nil
	}); err != nil { t.Fatal(err) }
	result, err := m.DeleteTasks([]string{"one","two"})
	if err != nil || result.Deleted != 2 || result.FilesDeleted != 2 {
		t.Fatalf("unexpected delete result: %#v %v", result, err)
	}
	for _, filename := range []string{one,two} {
		if _, err := os.Stat(filename); !os.IsNotExist(err) { t.Fatalf("MP4 was not actually removed: %s: %v", filename, err) }
	}
	if _, err := os.Stat(keep); err != nil { t.Fatalf("unselected MP4 was removed: %v",err) }
	snapshot := st.Snapshot()
	if len(snapshot.Downloads)!=1 || snapshot.Downloads[0].ID!="keep" || snapshot.Subscriptions[0].DownloadedCount!=0 {
		t.Fatalf("task list/count was not updated correctly: %#v",snapshot)
	}
	if _, err := os.Stat(filepath.Dir(keep)); err != nil { t.Fatal("folder should not be deleted",err) }
}

func TestDeleteTasksMissingFileAndNoPathStillDeleteRecords(t *testing.T) {
	m,st,dir,categoryID:=setupTaskDeleteTest(t)
	missing:=filepath.Join(dir,"downloads","动画节目","deleted.mp4")
	if err:=st.Update(func(s *model.State) error {
		s.Downloads=[]model.DownloadTask{
			{ID:"missing",Status:"completed",FilePath:missing,StorageCategoryID:categoryID},
			{ID:"failed",Status:"failed"},
			{ID:"queued",Status:"queued"},
		}
		return nil
	});err!=nil { t.Fatal(err) }
	result,err:=m.DeleteTasks([]string{"missing","failed","queued"})
	if err!=nil || result.Deleted!=3 || result.FilesDeleted!=0 {t.Fatalf("bad result: %#v %v",result,err)}
	if len(st.Snapshot().Downloads)!=0 {t.Fatal("task records remain")}
}

func TestDeleteTasksRejectsActiveOrMissingIDWithoutChangingFiles(t *testing.T) {
	m,st,dir,categoryID:=setupTaskDeleteTest(t)
	path:=createTaskDeleteFile(t,filepath.Join(dir,"downloads"),"keep")
	if err:=st.Update(func(s *model.State) error {
		s.Downloads=[]model.DownloadTask{{ID:"paused-active",Status:"paused"},{ID:"ready",Status:"completed",FilePath:path,StorageCategoryID:categoryID}}
		return nil
	});err!=nil {t.Fatal(err)}
	m.mu.Lock()
	m.active["paused-active"]=func(){}
	m.mu.Unlock()
	if _,err:=m.DeleteTasks([]string{"paused-active","ready"});err==nil {t.Fatal("active task was deleted")}
	m.mu.Lock()
	delete(m.active,"paused-active")
	m.mu.Unlock()
	if _,err:=m.DeleteTasks([]string{"non-existent","ready"});err==nil {t.Fatal("missing ID was silently ignored")}
	if _,err:=m.DeleteTasks([]string{"ready","ready"});err==nil {t.Fatal("duplicate IDs accepted")}
	if len(st.Snapshot().Downloads)!=2 {t.Fatal("rejected requests changed the task records")}
	if _,err:=os.Stat(path);err!=nil {t.Fatal("rejected request deleted a file",err)}
}

func TestDeleteTasksRejectsUnsafePathAndSharedFile(t *testing.T) {
	m,st,dir,categoryID:=setupTaskDeleteTest(t)
	inside:=createTaskDeleteFile(t,filepath.Join(dir,"downloads"),"video")
	outside:=createTaskDeleteFile(t,filepath.Join(dir,"elsewhere"),"outside")
	if err:=st.Update(func(s *model.State) error {
		s.Downloads=[]model.DownloadTask{
			{ID:"unsafe",Status:"completed",FilePath:outside,StorageCategoryID:categoryID},
			{ID:"safe",Status:"completed",FilePath:inside,StorageCategoryID:categoryID},
		}
		return nil
	});err!=nil {t.Fatal(err)}
	if _,err:=m.DeleteTasks([]string{"safe","unsafe"});err==nil || !strings.Contains(err.Error(),"保存目录") {t.Fatalf("unsafe file allowed: %v",err)}
	for _,path:=range []string{inside,outside} {if _,err:=os.Stat(path);err!=nil {t.Fatalf("files changed on validation failure: %s: %v",path,err)}}
	if err:=st.Update(func(s *model.State) error {
		s.Downloads=[]model.DownloadTask{
			{ID:"first",Status:"completed",FilePath:inside,StorageCategoryID:categoryID},
			{ID:"second",Status:"completed",FilePath:inside,StorageCategoryID:categoryID},
		}
		return nil
	});err!=nil {t.Fatal(err)}
	if _,err:=m.DeleteTasks([]string{"first"});err==nil {t.Fatal("shared file was removed")}
	if _,err:=os.Stat(inside);err!=nil {t.Fatal(err)}
	result,err:=m.DeleteTasks([]string{"first","second"})
	if err!=nil || result.Deleted!=2 || result.FilesDeleted!=1 {t.Fatalf("shared batch delete: %#v %v",result,err)}
}

func TestDeleteTasksRejectsSymlinkedVideo(t *testing.T) {
	m,st,dir,categoryID:=setupTaskDeleteTest(t)
	outside:=createTaskDeleteFile(t,filepath.Join(dir,"other"),"private")
	link:=filepath.Join(dir,"downloads","动画节目","symlink.mp4")
	if err:=os.MkdirAll(filepath.Dir(link),0755);err!=nil {t.Fatal(err)}
	if err:=os.Symlink(outside,link);err!=nil {t.Skipf("symlink not supported: %v",err)}
	if err:=st.Update(func(s *model.State) error {
		s.Downloads=[]model.DownloadTask{{ID:"link",Status:"completed",FilePath:link,StorageCategoryID:categoryID}}
		return nil
	});err!=nil {t.Fatal(err)}
	if _,err:=m.DeleteTasks([]string{"link"});err==nil {t.Fatal("symlink removed without verification")}
	if _,err:=os.Stat(outside);err!=nil {t.Fatal("symlink target should survive",err)}
}

func TestDeleteTasksRollsBackRenamesIfDatabaseSaveFails(t *testing.T) {
	m,st,dir,categoryID:=setupTaskDeleteTest(t)
	file:=createTaskDeleteFile(t,filepath.Join(dir,"downloads"),"rollback")
	if err:=st.Update(func(s *model.State) error {
		s.Downloads=[]model.DownloadTask{{ID:"rollback",Status:"completed",FilePath:file,StorageCategoryID:categoryID}}
		return nil
	});err!=nil {t.Fatal(err)}
	if err:=st.Close();err!=nil {t.Fatal(err)}
	if _,err:=m.DeleteTasks([]string{"rollback"});err==nil {t.Fatal("expected a DB persistence error")}
	if _,err:=os.Stat(file);err!=nil {t.Fatalf("file not restored: %v",err)}
	if len(st.Snapshot().Downloads)!=1 {t.Fatal("database rollback lost task")}
	pending,_:=filepath.Glob(filepath.Join(filepath.Dir(file),".*.pending"))
	if len(pending)!=0 {t.Fatalf("staged files left behind: %v",pending)}
}

func TestDeleteLastEpisodeRemovesEmptySeriesFolder(t *testing.T) {
	m, st, dir, categoryID := setupTaskDeleteTest(t)
	root := filepath.Join(dir, "downloads")
	last := createTaskDeleteFile(t, root, "last")
	seriesDir := filepath.Dir(last)
	if err := st.Update(func(state *model.State) error {
		state.Downloads = []model.DownloadTask{{
			ID: "last", Title: "last", Status: "completed",
			FilePath: last, StorageCategoryID: categoryID,
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := m.DeleteTasks([]string{"last"})
	if err != nil || result.Deleted != 1 || result.FilesDeleted != 1 || result.DirectoriesDeleted != 1 {
		t.Fatalf("deleting the last MP4 should clean one series directory: %#v %v", result, err)
	}
	if _, err := os.Stat(seriesDir); !os.IsNotExist(err) {
		t.Fatalf("empty series directory remains: %s, %v", seriesDir, err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("configured download root must never be deleted: %v", err)
	}
}

func TestDeleteSelectedVideoKeepsSeriesFolderAndOtherEpisodes(t *testing.T) {
	m, st, dir, categoryID := setupTaskDeleteTest(t)
	root := filepath.Join(dir, "downloads")
	selected := createTaskDeleteFile(t, root, "episode1")
	untouched := createTaskDeleteFile(t, root, "episode2")
	if err := st.Update(func(state *model.State) error {
		state.Downloads = []model.DownloadTask{
			{ID: "episode1", Status: "completed", FilePath: selected, StorageCategoryID: categoryID},
			{ID: "episode2", Status: "completed", FilePath: untouched, StorageCategoryID: categoryID},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := m.DeleteTasks([]string{"episode1"})
	if err != nil || result.FilesDeleted != 1 || result.DirectoriesDeleted != 0 {
		t.Fatalf("unexpected result deleting one of two episodes: %#v %v", result, err)
	}
	if _, err := os.Stat(selected); !os.IsNotExist(err) {
		t.Fatalf("selected MP4 is still present: %v", err)
	}
	if _, err := os.Stat(untouched); err != nil {
		t.Fatalf("unselected MP4 must be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(untouched)); err != nil {
		t.Fatalf("shared series folder must be preserved: %v", err)
	}
}

func TestBatchDeleteLastVideosRemovesTheirDistinctDirectories(t *testing.T) {
	m, st, dir, categoryID := setupTaskDeleteTest(t)
	root := filepath.Join(dir, "downloads")
	a := filepath.Join(root, "A", "first.mp4")
	b := filepath.Join(root, "B", "second.mp4")
	keep := filepath.Join(root, "C", "keep.mp4")
	for _, path := range []string{a, b, keep} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Update(func(state *model.State) error {
		state.Downloads = []model.DownloadTask{
			{ID: "a", Status: "completed", FilePath: a, StorageCategoryID: categoryID},
			{ID: "b", Status: "completed", FilePath: b, StorageCategoryID: categoryID},
			{ID: "keep", Status: "completed", FilePath: keep, StorageCategoryID: categoryID},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := m.DeleteTasks([]string{"a", "b"})
	if err != nil || result.Deleted != 2 || result.FilesDeleted != 2 || result.DirectoriesDeleted != 2 {
		t.Fatalf("batch directory cleanup failed: %#v %v", result, err)
	}
	for _, folder := range []string{filepath.Dir(a), filepath.Dir(b)} {
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			t.Fatalf("deleted item's folder still exists: %s %v", folder, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unselected video missing: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("download root removed: %v", err)
	}
}

func TestEmptyDirectoryCleanupPreservesUnrelatedFiles(t *testing.T) {
	m, st, dir, categoryID := setupTaskDeleteTest(t)
	root := filepath.Join(dir, "downloads")
	video := createTaskDeleteFile(t, root, "episode")
	parent := filepath.Dir(video)
	other := filepath.Join(parent, "个人笔记.txt")
	if err := os.WriteFile(other, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(state *model.State) error {
		state.Downloads = []model.DownloadTask{{
			ID: "episode", Status: "completed", FilePath: video, StorageCategoryID: categoryID,
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := m.DeleteTasks([]string{"episode"})
	if err != nil || result.FilesDeleted != 1 || result.DirectoriesDeleted != 0 {
		t.Fatalf("an unrelated file must prevent whole-folder deletion: %#v %v", result, err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("user file must be preserved: %v", err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("directory with a user file must remain: %v", err)
	}
}

func TestDeleteNestedEpisodePrunesParentsButNotCategoryRoot(t *testing.T) {
	m, st, dir, categoryID := setupTaskDeleteTest(t)
	root := filepath.Join(dir, "downloads")
	season := filepath.Join(root, "动画", "第一季")
	video := filepath.Join(season, "1.mp4")
	if err := os.MkdirAll(season, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video, []byte("MP4"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(state *model.State) error {
		state.Downloads = []model.DownloadTask{{
			ID: "nested", Status: "completed", FilePath: video, StorageCategoryID: categoryID,
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := m.DeleteTasks([]string{"nested"})
	if err != nil || result.DirectoriesDeleted != 2 {
		t.Fatalf("nested empty directories not removed: %#v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "动画")); !os.IsNotExist(err) {
		t.Fatalf("empty parent folder remains: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("category root removed: %v", err)
	}
}

func TestDirectoryCleanupProtectsEveryConfiguredCategoryRoot(t *testing.T) {
	m, st, dir, defaultCategoryID := setupTaskDeleteTest(t)
	root := filepath.Join(dir, "downloads")
	otherRoot := filepath.Join(root, "独立分类")
	if _, err := m.AddCategory("独立分类", "", otherRoot); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(otherRoot, "episode.mp4")
	if err := os.WriteFile(video, []byte("MP4"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(state *model.State) error {
		state.Downloads = []model.DownloadTask{{
			ID: "overlapping", Status: "completed", FilePath: video, StorageCategoryID: defaultCategoryID,
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := m.DeleteTasks([]string{"overlapping"})
	if err != nil || result.FilesDeleted != 1 || result.DirectoriesDeleted != 0 {
		t.Fatalf("configured nested root was not protected: %#v %v", result, err)
	}
	if _, err := os.Stat(otherRoot); err != nil {
		t.Fatalf("other configured category root must remain: %v", err)
	}
}
