//go:build nydus
// +build nydus

package client

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/namespaces"
	nydusify "github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/testutil/integration"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNydusIntegration(t *testing.T) {
	testIntegration(
		t,
		testBuildExportNydusWithHybrid,
	)
}

func testBuildExportNydusWithHybrid(t *testing.T, sb integration.Sandbox) {
	integration.SkipIfDockerd(t, sb, "nydus build export")
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	var (
		imageService = client.ImageService()
		contentStore = client.ContentStore()
		ctx          = namespaces.WithNamespace(sb.Context(), "buildkit")
	)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	buildNydus := func(file string) {
		orgImage := "docker.io/library/alpine:latest"
		baseDef := llb.Image(orgImage).Run(llb.Args([]string{"/bin/touch", "/" + file}))
		def, err := baseDef.Marshal(sb.Context())
		require.NoError(t, err)

		target := registry + "/nydus/alpine:" + identity.NewID()
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":           target,
						"push":           "true",
						"compression":    "nydus",
						"oci-mediatypes": "true",
					},
				},
			},
		}, nil)
		require.NoError(t, err)

		img, err := imageService.Get(ctx, target)
		require.NoError(t, err)

		manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
		require.NoError(t, err)

		require.Equal(t, len(manifest.Layers), 3)
		require.Equal(t, "true", manifest.Layers[0].Annotations[nydusify.LayerAnnotationNydusBlob])
		require.Equal(t, "true", manifest.Layers[1].Annotations[nydusify.LayerAnnotationNydusBlob])
		require.Equal(t, "true", manifest.Layers[2].Annotations[nydusify.LayerAnnotationNydusBootstrap])
	}

	buildOther := func(file string, compType compression.Type, forceCompression bool) {
		orgImage := "docker.io/library/alpine:latest"
		baseDef := llb.Image(orgImage).Run(llb.Args([]string{"/bin/touch", "/" + file}))
		def, err := baseDef.Marshal(sb.Context())
		require.NoError(t, err)

		mediaTypes := map[compression.Type]string{
			compression.Gzip: ocispecs.MediaTypeImageLayerGzip,
			compression.Zstd: ocispecs.MediaTypeImageLayer + "+zstd",
		}
		target := fmt.Sprintf("%s/%s/alpine:%s", registry, compType, identity.NewID())
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":              target,
						"push":              "true",
						"compression":       compType.String(),
						"oci-mediatypes":    "true",
						"force-compression": strconv.FormatBool(forceCompression),
					},
				},
			},
		}, nil)
		require.NoError(t, err)

		img, err := imageService.Get(ctx, target)
		require.NoError(t, err)

		manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
		require.NoError(t, err)

		require.Equal(t, 2, len(manifest.Layers))
		require.Equal(t, mediaTypes[compType], manifest.Layers[0].MediaType)
		require.Equal(t, mediaTypes[compType], manifest.Layers[1].MediaType)
	}

	// Make sure that the nydus compression layer is not mixed with other
	// types of compression layers in an image.
	buildNydus("foo")
	buildOther("foo", compression.Gzip, false)
	buildOther("foo", compression.Zstd, true)

	buildOther("bar", compression.Gzip, false)
	buildOther("bar", compression.Zstd, true)
	buildNydus("bar")
}
