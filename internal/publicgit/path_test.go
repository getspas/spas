package publicgit

import "github.com/getspas/spas/internal/pathmodel"

func pathmodelForTest(value string) (pathmodel.Path, error) {
	return pathmodel.Parse(value)
}
