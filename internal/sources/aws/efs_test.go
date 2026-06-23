package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"

	"alertkube/internal/alert"
)

type fakeEFS struct {
	pages []*efs.DescribeFileSystemsOutput
	idx   int
	err   error
}

func (f *fakeEFS) DescribeFileSystems(_ context.Context, _ *efs.DescribeFileSystemsInput, _ ...func(*efs.Options)) (*efs.DescribeFileSystemsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func fileSystem(id string, state efstypes.LifeCycleState) efstypes.FileSystemDescription {
	return efstypes.FileSystemDescription{
		FileSystemId:   awssdk.String(id),
		Name:           awssdk.String(id + "-name"),
		LifeCycleState: state,
	}
}

func TestEvaluateFileSystem(t *testing.T) {
	cases := []struct {
		name         string
		fs           efstypes.FileSystemDescription
		wantEmit     bool
		wantResolved bool
	}{
		{"error critical", fileSystem("f", efstypes.LifeCycleStateError), true, false},
		{"available resolves", fileSystem("f", efstypes.LifeCycleStateAvailable), true, true},
		{"creating resolves", fileSystem("f", efstypes.LifeCycleStateCreating), true, true},
		{"deleting resolves", fileSystem("f", efstypes.LifeCycleStateDeleting), true, true},
		{"empty id skipped", fileSystem("", efstypes.LifeCycleStateError), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateFileSystem("us-east-1", tc.fs, emit)
			if !tc.wantEmit {
				if len(*got) != 0 {
					t.Fatalf("expected no emit, got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindEFSFileSystem {
				t.Errorf("kind = %s, want EFSFileSystem", a.Kind)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != alert.SeverityCritical {
				t.Errorf("severity = %q, want critical", a.Severity)
			}
		})
	}
}

func TestEFSSourcePoll(t *testing.T) {
	fake := &fakeEFS{pages: []*efs.DescribeFileSystemsOutput{{
		FileSystems: []efstypes.FileSystemDescription{
			fileSystem("good", efstypes.LifeCycleStateAvailable),
			fileSystem("bad", efstypes.LifeCycleStateError),
		},
	}}}
	src := &efsSource{regions: []efsRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
