package main

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/gitmerge"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizingpostgres "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/postgres"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func composeSynthesisManuscriptReview(runtime *apiSynthesisRuntimeComponents, d apiSynthesisDependencies, store *organizingpostgres.GORMSynthesisStore, sources *organizingowner.GORMSynthesisSourceReader, handler *organizinghttp.SynthesisHandler, definitions *workflowapp.DefinitionRegistry) (*organizinghttp.SynthesisHandler, error) {
	if d.ManuscriptRoots == nil {
		return handler, nil
	} // 历史组装测试夹具
	workspaces, ok := d.Workspaces.(localfs.WorkspaceRepository)
	if !ok {
		return nil, synthesisAPICompositionUnavailable("manuscript workspace owner is unavailable")
	}
	files, err := localfs.NewReader(workspaces)
	if err != nil {
		return nil, err
	}
	roots, err := organizingowner.NewSynthesisManuscriptRoot(d.ManuscriptRoots, files)
	if err != nil {
		return nil, err
	}
	merge, err := gitmerge.New(gitcli.New(""))
	if err != nil {
		return nil, err
	}
	manuscripts, err := organizingpostgres.NewSynthesisManuscriptRuntime(organizingpostgres.SynthesisManuscriptRuntimeDependencies{
		Pool: runtime.pool, Candidates: store, Models: runtime.store, Executions: runtime.store, Roots: roots,
		Service: organizingapp.SynthesisDependencies{Store: store, Sources: sources, Publications: d.Publications, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}},
		Storage: organizingpostgres.SynthesisManuscriptStoreDependencies{Files: roots, Mapper: manuscript.Mapper{}, Merge: merge, IDs: foundation.UUIDGenerator{}, Clock: foundation.SystemClock{}},
	})
	if err != nil {
		return nil, err
	}
	human, err := workflowapp.NewRuntimeHumanCoordinator(d.Runtime)
	if err != nil {
		return nil, err
	}
	sourceReviewStore, err := organizingpostgres.NewGORMSynthesisManuscriptSourceReviewStore(manuscripts, runtime.modelRuns, manuscript.Mapper{})
	if err != nil {
		return nil, err
	}
	sourceReviews, err := organizingpostgres.NewSynthesisSourceReviewRead(sourceReviewStore, sources)
	if err != nil {
		return nil, err
	}
	if err := sourceReviewStore.ConfigureSourceReviewCommands(d.Runtime, definitions); err != nil {
		return nil, err
	}
	return handler.WithManuscriptReviews(manuscripts, human).WithCandidateRemerge(manuscripts).WithHistoricalRepublish(manuscripts).WithSourceReviews(sourceReviews).WithSourceReviewCommands(sourceReviewStore), nil
}
