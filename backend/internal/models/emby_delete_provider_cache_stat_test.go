package models

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/v115open"
)

type embyDeleteStatCache115Client struct {
	*embyDelete115Stub
	read func(context.Context, string) (*v115open.FileDetail, error)
}

func (c *embyDeleteStatCache115Client) GetFsDetailByCidForDeletion(ctx context.Context, id string) (*v115open.FileDetail, error) {
	return c.read(ctx, id)
}

func embyStatCacheDetail(file EmbyFrozenFile) *v115open.FileDetail {
	return &v115open.FileDetail{
		FileId: file.FileID, FileName: file.FileName, Path: strings.TrimPrefix(file.Path, "/"),
		Paths: []v115open.FileDetailPath{{FileId: file.ParentID}}, Sha1: file.SHA1,
		Utime: strconv.FormatInt(file.MTime, 10), FileSizeByte: file.FileSize, FileCategory: v115open.TypeFile,
	}
}

func embyStatCacheRemote(file EmbyFrozenFile) EmbyRemoteFile {
	return EmbyRemoteFile{
		FileID: embyFrozenPhysicalID(file), ParentID: file.ParentID, FileName: file.FileName,
		Path: file.Path, PickCode: file.PickCode, SHA1: file.SHA1, FileSize: file.FileSize, MTime: file.MTime,
		OpenlistObjectID: file.OpenlistObjectID, OpenlistSHA1: file.OpenlistSHA1, OpenlistMD5: file.OpenlistMD5,
	}
}

func TestEmbyDeleteProviderStatSeedPreservesFullReadAndDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, release := beginEmbyDirectoryChecks(t.Context())
		defer release()
		file := embyProviderTestFile(SourceType115)
		remote := embyStatCacheRemote(file)
		remote.PickCode, remote.FileCreatedAt = "original-pickcode", 1600000000
		readAt := time.Now()
		time.Sleep(10 * time.Second)
		if err := SeedEmbyDeletionStat(ctx, file, remote, readAt); err != nil {
			t.Fatal(err)
		}
		reads := 0
		provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity,
			v115: &embyDeleteStatCache115Client{read: func(context.Context, string) (*v115open.FileDetail, error) {
				reads++
				return embyStatCacheDetail(file), nil
			}}}
		for range 2 {
			got, at, found, err := LookupEmbyDeletionStat(ctx, file)
			if err != nil || !found || got != remote || !at.Equal(readAt) {
				t.Fatalf("remote=%+v at=%v found=%v err=%v", got, at, found, err)
			}
			if got, err := provider.Stat(ctx, file); err != nil || got != remote || reads != 0 {
				t.Fatalf("seed repeated detail: remote=%+v reads=%d err=%v", got, reads, err)
			}
			time.Sleep(10 * time.Second)
		}
		if _, err := provider.Stat(ctx, file); err != nil || reads != 1 {
			t.Fatalf("expired seed failed to refresh: reads=%d err=%v", reads, err)
		}
		if _, err := provider.Stat(ctx, file); err != nil || reads != 1 {
			t.Fatalf("fresh detail not shared: reads=%d err=%v", reads, err)
		}
	})
}

func TestEmbyDeleteProviderStatUsesCompleteStableIDListing(t *testing.T) {
	for _, mode := range []string{"complete", "missing hash", "missing time", "missing pickcode", "conflicting identity", "duplicate stable ID"} {
		t.Run(mode, func(t *testing.T) {
			ctx, release := beginEmbyDirectoryChecks(t.Context())
			defer release()
			file := embyProviderTestFile(SourceType115)
			if mode == "missing pickcode" {
				file.PickCode = "known-pickcode"
			}
			reads, lists := 0, 0
			client := &embyDeleteStatCache115Client{embyDelete115Stub: &embyDelete115Stub{
				list: func(int) (*v115open.FileListResp, error) {
					lists++
					response := embyProviderBatchList115([]EmbyFrozenFile{file})
					switch mode {
					case "missing hash":
						response.Data[0].Sha1 = ""
					case "missing time":
						response.Data[0].Utime = 0
					case "conflicting identity":
						response.Data[0].Sha1 = "replacement"
					case "duplicate stable ID":
						response.Data = append(response.Data, response.Data[0])
						response.Count++
					}
					return response, nil
				}}, read: func(context.Context, string) (*v115open.FileDetail, error) {
				reads++
				detail := embyStatCacheDetail(file)
				detail.PickCode = file.PickCode
				return detail, nil
			}}
			provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity, v115: client}
			_, err := provider.List(ctx, file)
			if mode == "duplicate stable ID" {
				if !errors.Is(err, ErrEmbyDeleteUnverified) {
					t.Fatalf("ambiguous list accepted: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			got, err := provider.Stat(ctx, file)
			wantReads := 1
			if mode == "complete" || mode == "conflicting identity" {
				wantReads = 0
			}
			if err != nil || reads != wantReads || lists != 1 {
				t.Fatalf("stat=%+v reads=%d lists=%d err=%v", got, reads, lists, err)
			}
			if mode == "conflicting identity" && embyProviderFileMatches(file, got) {
				t.Fatal("replacement identity authorized")
			}
			if mode == "complete" {
				minimal := EmbyFrozenFile{SourceType: file.SourceType, AccountID: file.AccountID, AccountIdentity: file.AccountIdentity, FileID: file.FileID}
				if got, err := provider.Stat(ctx, minimal); err != nil || got.FileName != file.FileName || reads != 0 {
					t.Fatalf("stable-ID-only lookup=%+v reads=%d err=%v", got, reads, err)
				}
			}
		})
	}
}

func TestEmbyDeleteProviderStatCacheIsolationAndInvalidation(t *testing.T) {
	for _, mode := range []string{"account", "principal", "source", "stable ID", "new attempt", "configuration", "file write", "directory write", "release", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx, release := beginEmbyDirectoryChecks(base)
			defer release()
			file := embyProviderTestFile(SourceType115)
			if err := SeedEmbyDeletionStat(ctx, file, embyStatCacheRemote(file), time.Now()); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "account":
				file.AccountID++
			case "principal":
				file.AccountIdentity = "different"
			case "source":
				file.SourceType = SourceTypeBaiduPan
			case "stable ID":
				file.FileID = "other"
			case "new attempt":
				var done func()
				ctx, done = beginEmbyDirectoryChecks(base)
				defer done()
			case "configuration":
				end := BeginSyncPositionMutation()
				end()
			case "file write":
				embyInvalidateDeletionListings(ctx, file, false)
			case "directory write":
				root := file
				root.Path, root.FileName, root.FileID = "/", "media", "7"
				embyInvalidateDeletionListings(ctx, root, true)
			case "release":
				release()
			case "cancel":
				cancel()
			}
			_, _, found, err := LookupEmbyDeletionStat(ctx, file)
			if found || mode == "release" && !errors.Is(err, ErrEmbyDeleteUnverified) || mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("found=%v err=%v", found, err)
			}
		})
	}
}

func TestEmbyDeleteProviderStatFailureAndWriteConfirmation(t *testing.T) {
	for _, mode := range []string{"failed read", "cancelled read", "write during read", "failed delete"} {
		t.Run(mode, func(t *testing.T) {
			ctx, release := beginEmbyDirectoryChecks(t.Context())
			defer release()
			file := embyProviderTestFile(SourceType115)
			reads, writes := 0, 0
			client := &embyDeleteStatCache115Client{embyDelete115Stub: &embyDelete115Stub{
				delete: func([]string, string) (bool, error) {
					writes++
					return false, errors.New("ambiguous write")
				}}, read: func(context.Context, string) (*v115open.FileDetail, error) {
				reads++
				if writes > 0 {
					return nil, v115open.NewOpenAPIError(430004, "gone")
				}
				if reads == 1 {
					switch mode {
					case "failed read":
						return nil, errors.New("transport failed")
					case "cancelled read":
						return nil, context.Canceled
					case "write during read":
						embyInvalidateDeletionListings(ctx, file, false)
					}
				}
				return embyStatCacheDetail(file), nil
			}}
			provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity, v115: client}
			_, err := provider.Stat(ctx, file)
			if mode == "failed delete" {
				if err != nil {
					t.Fatal(err)
				}
				if ok, err := provider.Delete(ctx, file, func() error { return nil }); ok || err == nil || reads != 1 || writes != 1 {
					t.Fatalf("delete=%v reads=%d writes=%d err=%v", ok, reads, writes, err)
				}
				if _, err := provider.Stat(ctx, file); !errors.Is(err, ErrEmbyRemoteFileAbsent) || reads != 2 {
					t.Fatalf("write confirmation reused old stat: reads=%d err=%v", reads, err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed or superseded read accepted")
			}
			if _, err := provider.Stat(ctx, file); err != nil || reads != 2 {
				t.Fatalf("failed read reused: reads=%d err=%v", reads, err)
			}
		})
	}
}

func TestEmbyDeleteProviderStatSeedCannotOutliveWriteOrFinalGuard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, release := beginEmbyDirectoryChecks(t.Context())
		defer release()
		file := embyProviderTestFile(SourceType115)
		remote, readAt := embyStatCacheRemote(file), time.Now()
		time.Sleep(time.Second)
		embyInvalidateDeletionListings(ctx, file, false)
		if err := SeedEmbyDeletionStat(ctx, file, remote, readAt); !errors.Is(err, ErrEmbyDeleteUnverified) {
			t.Fatalf("old in-flight read seeded after write: %v", err)
		}
		if err := SeedEmbyDeletionStat(ctx, file, remote, time.Now()); err != nil {
			t.Fatal(err)
		}
		writes, reads := 0, 0
		client := &embyDeleteStatCache115Client{embyDelete115Stub: &embyDelete115Stub{
			delete: func([]string, string) (bool, error) { writes++; return true, nil },
		}, read: func(context.Context, string) (*v115open.FileDetail, error) {
			reads++
			return embyStatCacheDetail(file), nil
		}}
		provider := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity, v115: client}
		time.Sleep(29 * time.Second)
		if ok, err := provider.Delete(ctx, file, func() error { time.Sleep(2 * time.Second); return nil }); ok || !errors.Is(err, ErrEmbyDeleteUnverified) || writes != 0 || reads != 0 {
			t.Fatalf("expired guard wrote or renewed seed: ok=%v writes=%d reads=%d err=%v", ok, writes, reads, err)
		}
		if _, err := provider.Stat(context.WithValue(ctx, embyDeletionFinalGuardKey{}, true), file); !errors.Is(err, ErrEmbyDeleteUnverified) || reads != 0 {
			t.Fatalf("final guard fetched missing stat: reads=%d err=%v", reads, err)
		}
	})
}
