package geoboundaries

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
)

const (
	testBoundaryID = "CHN-ADM0-351020"
	testShapeID    = "351020B83567386155957"
	testSourceURL  = "https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson"
	testMediaURL   = "https://media.githubusercontent.com/media/wmgeolab/geoBoundaries/9469f09/releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson"
	testCRSObject  = `{"type":"name","properties":{"name":"urn:ogc:def:crs:OGC:1.3:CRS84"}}`
)

func TestDecodeMetadataAcceptsCurrentContractAndExtension(t *testing.T) {
	payload := strings.TrimSuffix(string(validMetadataPayload()), "}") +
		`,"providerNote":{"revision":"2026-08-29"}}`
	value, err := decodeMetadata([]byte(payload))
	if err != nil || value.BoundaryID != testBoundaryID ||
		value.Source != expectedSource || value.License != expectedLicense {
		t.Fatalf("decodeMetadata()=%+v error=%v", value, err)
	}
	media, err := fixedMediaURL(value.SimplifiedGeometry)
	if err != nil || media != testMediaURL {
		t.Fatalf("fixedMediaURL()=%q error=%v", media, err)
	}
}

func TestDecodeMetadataRejectsInvalidIdentityYearSourceAndLicense(t *testing.T) {
	base := string(validMetadataPayload())
	cases := map[string]string{
		"zero-boundary-id":    strings.Replace(base, testBoundaryID, "CHN-ADM0-000000", 1),
		"bad-boundary-id":     strings.Replace(base, testBoundaryID, "CHN-ADM0-351020x", 1),
		"missing-boundary-id": strings.Replace(base, `"boundaryID":"`+testBoundaryID+`",`, "", 1),
		"null-boundary-id": strings.Replace(base, `"boundaryID":"`+testBoundaryID+`"`,
			`"boundaryID":null`, 1),
		"duplicate-id": strings.Replace(base, `"boundaryID":"`+testBoundaryID+`"`,
			`"boundaryID":"`+testBoundaryID+`","boundaryID":"`+testBoundaryID+`"`, 1),
		"case-alias-id": strings.Replace(base, `"boundaryID":"`+testBoundaryID+`"`,
			`"BoundaryID":"`+testBoundaryID+`"`, 1),
		"year-format": strings.Replace(base, `"boundaryYearRepresented":"2019"`,
			`"boundaryYearRepresented":"20x9"`, 1),
		"year-too-old": strings.Replace(base, `"boundaryYearRepresented":"2019"`,
			`"boundaryYearRepresented":"1899"`, 1),
		"year-future": strings.Replace(base, `"boundaryYearRepresented":"2019"`,
			`"boundaryYearRepresented":"9999"`, 1),
		"year-whitespace": strings.Replace(base, `"boundaryYearRepresented":"2019"`,
			`"boundaryYearRepresented":" 2019"`, 1),
		"old-source":         strings.Replace(base, expectedSource, "geoBoundaries", 1),
		"source-whitespace":  strings.Replace(base, expectedSource, " "+expectedSource, 1),
		"license-whitespace": strings.Replace(base, expectedLicense, expectedLicense+" ", 1),
		"wrong-license":      strings.Replace(base, expectedLicense, "CC BY 4.0", 1),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			assertProviderError(t, decodeMetadataError([]byte(payload)))
		})
	}
}

func TestDecodeMetadataRejectsUnicodeFoldCriticalKeys(t *testing.T) {
	base := string(validMetadataPayload())
	source := `"boundarySource":"` + expectedSource + `"`
	longSource := `"boundaryſource":"` + expectedSource + `"`
	cases := map[string]string{
		"source-long-s-only": strings.Replace(base, source, longSource, 1),
		"source-canonical-first": strings.Replace(base, source,
			source+`,`+longSource, 1),
		"source-long-s-first": strings.Replace(base, source,
			longSource+`,`+source, 1),
		"iso-long-s-only": strings.Replace(base, `"boundaryISO":"CHN"`,
			`"boundaryIſO":"CHN"`, 1),
		"license-long-s-only": strings.Replace(base, `"boundaryLicense":`,
			`"boundaryLicenſe":`, 1),
		"geometry-url-long-s-only": strings.Replace(base, `"simplifiedGeometryGeoJSON":`,
			`"ſimplifiedGeometryGeoJSON":`, 1),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			assertProviderError(t, decodeMetadataError([]byte(payload)))
		})
	}
}

func TestBoundaryRejectsUnicodeFoldMetadataBeforeGeometryDownload(t *testing.T) {
	metadataPayload := strings.Replace(string(validMetadataPayload()),
		`"boundarySource":`, `"boundaryſource":`, 1)
	provider, requests := boundaryProviderWithMetadata(t, []byte(metadataPayload),
		string(validGeometryDocument().payload()), "")
	value, err := provider.Boundary(context.Background())
	if !errors.Is(err, domain.ErrProviderUnavailable) || requests.Load() != 1 ||
		value.BoundaryID != "" || value.Digest != "" || len(value.InputReferences) != 0 {
		t.Fatalf("Boundary()=%+v error=%v requests=%d", value, err, requests.Load())
	}
}

func TestFixedMediaURLRejectsHostAndMovingBranch(t *testing.T) {
	values := []string{
		"https://attacker.test/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson",
		"https://github.com/wmgeolab/geoBoundaries/raw/main/releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson",
		"https://github.com/other/geoBoundaries/raw/9469f09/releaseData/gbOpen/CHN/ADM0/geoBoundaries-CHN-ADM0_simplified.geojson",
	}
	for _, value := range values {
		if _, err := fixedMediaURL(value); err == nil {
			t.Fatalf("fixedMediaURL(%q) 未拒绝", value)
		}
	}
}

func TestBoundaryRejectsSameOriginGeometryRedirect(t *testing.T) {
	provider, requests := boundaryProvider(t, string(validGeometryDocument().payload()),
		"https://media.githubusercontent.com/redirected.geojson")
	_, err := provider.Boundary(context.Background())
	if !errors.Is(err, domain.ErrProviderUnavailable) || requests.Load() != 2 {
		t.Fatalf("Boundary() error=%v requests=%d", err, requests.Load())
	}
}

func TestBoundaryEvidenceBindsURLDigestAndFullShapeID(t *testing.T) {
	payload := validGeometryDocument().payload()
	metadataPayload := validMetadataPayload()
	provider, requests := boundaryProvider(t, string(payload), "")
	value, err := provider.Boundary(context.Background())
	digest := sha256.Sum256(payload)
	digestHex := hex.EncodeToString(digest[:])
	metadataDigest := sha256.Sum256(metadataPayload)
	metadataDigestHex := hex.EncodeToString(metadataDigest[:])
	if err != nil || requests.Load() != 2 || value.Reference != testSourceURL ||
		value.Digest != digestHex || len(value.InputReferences) != 2 {
		t.Fatalf("Boundary()=%+v error=%v requests=%d", value, err, requests.Load())
	}
	assertBoundaryBindingReference(t, value.InputReferences[1], metadataDigestHex, digestHex)
}

func TestRiskBoundaryProjectsValidatedCoverageAndGeometry(t *testing.T) {
	provider, requests := boundaryProvider(t, string(validGeometryDocument().payload()), "")
	value, err := provider.RiskBoundary(context.Background())
	if err != nil || requests.Load() != 2 {
		t.Fatalf("RiskBoundary()=%+v error=%v requests=%d", value, err, requests.Load())
	}
	if value.Coverage.BoundaryID != testBoundaryID || value.Coverage.RegionCode != "CN" ||
		value.Coverage.BoundaryType != "ADM0" || value.Coverage.BoundaryVersion != "2019" ||
		value.Geometry.Type != "MultiPolygon" || len(value.InputReferences) != 2 {
		t.Fatalf("RiskBoundary()=%+v", value)
	}
	if err = value.Validate(); err != nil {
		t.Fatalf("RiskBoundary() 未通过领域校验: %v", err)
	}
}

func TestBoundaryBindingReferenceChangesWithMetadataEvidence(t *testing.T) {
	value, err := decodeMetadata(validMetadataPayload())
	if err != nil {
		t.Fatal(err)
	}
	base := boundaryBindingReference(testMediaURL, value, testShapeID,
		strings.Repeat("a", 64), strings.Repeat("b", 64))
	variants := []metadata{value, value, value}
	variants[0].BoundaryYear = "2018"
	variants[1].Source = "different source"
	variants[2].License = "different license"
	for index, variant := range variants {
		if reference := boundaryBindingReference(testMediaURL, variant, testShapeID,
			strings.Repeat("a", 64), strings.Repeat("b", 64)); reference == base {
			t.Fatalf("metadata 证据变体 %d 未改变绑定引用", index)
		}
	}
	changedDigest := boundaryBindingReference(testMediaURL, value, testShapeID,
		strings.Repeat("c", 64), strings.Repeat("b", 64))
	if changedDigest == base {
		t.Fatal("metadata 响应摘要变化未改变绑定引用")
	}
}

func TestBoundaryRejectsMismatchedShapeIDBeforeReturningEvidence(t *testing.T) {
	document := validGeometryDocument()
	document.Properties = propertyFields("351021B83567386155957")
	provider, requests := boundaryProvider(t, string(document.payload()), "")
	value, err := provider.Boundary(context.Background())
	if !errors.Is(err, domain.ErrProviderUnavailable) || requests.Load() != 2 ||
		value.BoundaryID != "" || value.Digest != "" || len(value.InputReferences) != 0 {
		t.Fatalf("Boundary()=%+v error=%v requests=%d", value, err, requests.Load())
	}
}

func TestDecodeGeometryValidatesShapeIdentityAndClosedRings(t *testing.T) {
	value, err := decodeGeometry(validGeometryDocument().payload(), testBoundaryID)
	if err != nil || len(value.Value) == 0 || value.ShapeID != testShapeID {
		t.Fatalf("decodeGeometry()=%+v error=%v", value, err)
	}
	document := validGeometryDocument()
	document.Geometry = geometryFields(`[[[[116,39],[116.1,39],[116.1,39.1],[116.2,39]]]]`)
	_, err = decodeGeometry(document.payload(), testBoundaryID)
	assertProviderError(t, err)
}

func TestDecodeGeometryRejectsBoundaryAndShapeIDMismatch(t *testing.T) {
	cases := map[string]struct {
		boundaryID string
		shapeID    string
	}{
		"zero-boundary":        {boundaryID: "CHN-ADM0-000000", shapeID: "000000B1"},
		"boundary-suffix":      {boundaryID: "CHN-ADM0-351020x", shapeID: testShapeID},
		"wrong-shape-prefix":   {boundaryID: testBoundaryID, shapeID: "351021B83567386155957"},
		"shape-separator":      {boundaryID: testBoundaryID, shapeID: "351020-83567386155957"},
		"shape-numeric-tail":   {boundaryID: testBoundaryID, shapeID: "351020Babc"},
		"shape-missing-tail":   {boundaryID: testBoundaryID, shapeID: "351020B"},
		"shape-leading-prefix": {boundaryID: testBoundaryID, shapeID: "0351020B1"},
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			document := validGeometryDocument()
			document.Properties = propertyFields(item.shapeID)
			_, err := decodeGeometry(document.payload(), item.boundaryID)
			assertProviderError(t, err)
		})
	}
}

func TestBoundaryIDSuffixAcceptsOnlyNonZeroDigits(t *testing.T) {
	for value, want := range map[string]bool{
		"CHN-ADM0-1": true, "CHN-ADM0-0001": true,
		"CHN-ADM0-0": false, "CHN-ADM0-000000": false,
		"CHN-ADM0-": false, "CHN-ADM0-1x": false, "CHN-ADM0- 1": false,
	} {
		_, got := boundaryIDSuffix(value)
		if got != want {
			t.Fatalf("boundaryIDSuffix(%q) valid=%v want=%v", value, got, want)
		}
	}
}

func TestDecodeGeometryValidatesRemainingIdentity(t *testing.T) {
	base := validGeometryDocument()
	cases := map[string]string{
		"shape-name":  strings.Replace(base.Properties, `"shapeName":"China"`, `"shapeName":"PRC"`, 1),
		"shape-iso":   strings.Replace(base.Properties, `"shapeISO":"CHN"`, `"shapeISO":"CN"`, 1),
		"shape-group": strings.Replace(base.Properties, `"shapeGroup":"CHN"`, `"shapeGroup":"Asia"`, 1),
		"shape-type":  strings.Replace(base.Properties, `"shapeType":"ADM0"`, `"shapeType":"ADM1"`, 1),
	}
	for name, properties := range cases {
		t.Run(name, func(t *testing.T) {
			document := base
			document.Properties = properties
			_, err := decodeGeometry(document.payload(), testBoundaryID)
			assertProviderError(t, err)
		})
	}
}

func TestDecodeGeometryAcceptsMissingOrExactCRS84(t *testing.T) {
	for name, crs := range map[string]string{"missing": "", "crs84": testCRSObject} {
		t.Run(name, func(t *testing.T) {
			document := validGeometryDocument()
			document.CRS = crs
			if _, err := decodeGeometry(document.payload(), testBoundaryID); err != nil {
				t.Fatalf("decodeGeometry() error=%v", err)
			}
		})
	}
}

func TestDecodeGeometryRejectsInvalidCRS(t *testing.T) {
	cases := map[string]string{
		"null":           "null",
		"epsg-3857":      `{"type":"name","properties":{"name":"urn:ogc:def:crs:EPSG::3857"}}`,
		"epsg-4326":      `{"type":"name","properties":{"name":"urn:ogc:def:crs:EPSG::4326"}}`,
		"alias-name":     `{"type":"name","properties":{"Name":"urn:ogc:def:crs:OGC:1.3:CRS84"}}`,
		"alias-type":     `{"Type":"name","properties":{"name":"urn:ogc:def:crs:OGC:1.3:CRS84"}}`,
		"duplicate-name": `{"type":"name","properties":{"name":"urn:ogc:def:crs:OGC:1.3:CRS84","name":"urn:ogc:def:crs:OGC:1.3:CRS84"}}`,
		"duplicate-type": `{"type":"name","type":"name","properties":{"name":"urn:ogc:def:crs:OGC:1.3:CRS84"}}`,
		"extra-crs":      `{"type":"name","properties":{"name":"urn:ogc:def:crs:OGC:1.3:CRS84"},"extra":true}`,
		"extra-property": `{"type":"name","properties":{"name":"urn:ogc:def:crs:OGC:1.3:CRS84","extra":true}}`,
	}
	for name, crs := range cases {
		t.Run(name, func(t *testing.T) {
			document := validGeometryDocument()
			document.CRS = crs
			_, err := decodeGeometry(document.payload(), testBoundaryID)
			assertProviderError(t, err)
		})
	}
	document := validGeometryDocument()
	base := string(document.payload())
	payload := strings.Replace(base, `"crs":`, `"CRS":`, 1)
	_, err := decodeGeometry([]byte(payload), testBoundaryID)
	assertProviderError(t, err)
	duplicate := strings.Replace(base, `"crs":`+testCRSObject,
		`"crs":`+testCRSObject+`,"crs":`+testCRSObject, 1)
	_, err = decodeGeometry([]byte(duplicate), testBoundaryID)
	assertProviderError(t, err)
}

func TestDecodeGeometryRejectsCriticalDuplicateAndCaseAlias(t *testing.T) {
	base := string(validGeometryDocument().payload())
	cases := map[string]string{
		"collection-duplicate": strings.Replace(base, `"type":"FeatureCollection"`,
			`"type":"FeatureCollection","type":"FeatureCollection"`, 1),
		"collection-alias": strings.Replace(base, `"type":"FeatureCollection"`,
			`"Type":"FeatureCollection"`, 1),
		"feature-duplicate": strings.Replace(base, `"type":"Feature","properties"`,
			`"type":"Feature","type":"Feature","properties"`, 1),
		"feature-alias": strings.Replace(base, `"type":"Feature","properties"`,
			`"Type":"Feature","properties"`, 1),
		"properties-duplicate": strings.Replace(base, `"shapeID":"`+testShapeID+`"`,
			`"shapeID":"`+testShapeID+`","shapeID":"`+testShapeID+`"`, 1),
		"properties-alias": strings.Replace(base, `"shapeID":"`+testShapeID+`"`,
			`"ShapeID":"`+testShapeID+`"`, 1),
		"properties-unicode-fold-alias": strings.Replace(base, `"shapeID":"`+testShapeID+`"`,
			`"ſhapeID":"`+testShapeID+`"`, 1),
		"properties-shape-id-missing": strings.Replace(base,
			`"shapeID":"`+testShapeID+`",`, "", 1),
		"properties-shape-id-null": strings.Replace(base, `"shapeID":"`+testShapeID+`"`,
			`"shapeID":null`, 1),
		"geometry-duplicate": strings.Replace(base, `"type":"MultiPolygon","coordinates"`,
			`"type":"MultiPolygon","type":"MultiPolygon","coordinates"`, 1),
		"geometry-alias": strings.Replace(base, `"type":"MultiPolygon","coordinates"`,
			`"Type":"MultiPolygon","coordinates"`, 1),
		"geometry-coordinates-duplicate": strings.Replace(base, `"coordinates":`,
			`"coordinates":[],"coordinates":`, 1),
		"geometry-coordinates-alias": strings.Replace(base, `"coordinates":`, `"Coordinates":`, 1),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeGeometry([]byte(payload), testBoundaryID)
			assertProviderError(t, err)
		})
	}
}

func TestDecodeGeometryRejectsNestedCoordinateSemanticDeclarations(t *testing.T) {
	base := validGeometryDocument()
	cases := map[string]geometryDocument{}
	geometryCRS := base
	geometryCRS.Geometry += `,"crs":{"type":"name","properties":{"name":"urn:ogc:def:crs:EPSG::3857"}}`
	cases["geometry-crs"] = geometryCRS
	geometrySRS := base
	geometrySRS.Geometry += `,"srsName":"EPSG:3857"`
	cases["geometry-srs-name"] = geometrySRS
	featureCRS := base
	featureCRS.Feature += `,"crs":{"type":"name","properties":{"name":"urn:ogc:def:crs:EPSG::3857"}}`
	cases["feature-crs"] = featureCRS
	featureAxis := base
	featureAxis.Feature += `,"axisOrder":"lat-lon"`
	cases["feature-axis-order"] = featureAxis
	collectionSRS := base
	collectionSRS.Collection += `,"srsName":"EPSG:3857"`
	cases["collection-srs-name"] = collectionSRS
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeGeometry(document.payload(), testBoundaryID)
			assertProviderError(t, err)
		})
	}
}

func TestDecodeGeometryRejectsFieldsOutsideCurrentContract(t *testing.T) {
	base := validGeometryDocument()
	cases := map[string]geometryDocument{}
	collection := base
	collection.Collection += `,"vendorCollection":{"version":1}`
	cases["collection"] = collection
	feature := base
	feature.Feature += `,"vendorFeature":true`
	cases["feature"] = feature
	properties := base
	properties.Properties += `,"vendorProperty":"blocked"`
	cases["properties"] = properties
	geometry := base
	geometry.Geometry += `,"bbox":[73,18,135,54]`
	cases["geometry"] = geometry
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeGeometry(document.payload(), testBoundaryID)
			assertProviderError(t, err)
		})
	}
}

func TestDecodeGeometryRejectsNullAndMissingOrdinates(t *testing.T) {
	cases := map[string]string{
		"null-longitude":  `[[[[null,0],[1,0],[1,1],[null,0]]]]`,
		"null-latitude":   `[[[[0,null],[1,0],[1,1],[0,null]]]]`,
		"single-ordinate": `[[[[0],[1,0],[1,1],[0]]]]`,
		"empty-position":  `[[[[],[1,0],[1,1],[]]]]`,
		"null-position":   `[[[null,[1,0],[1,1],null]]]`,
		"empty-polygon":   `[[],[[[0,0],[1,0],[1,1],[0,0]]]]`,
		"null-polygon":    `[null,[[[0,0],[1,0],[1,1],[0,0]]]]`,
	}
	for name, coordinates := range cases {
		t.Run(name, func(t *testing.T) {
			document := validGeometryDocument()
			document.Geometry = geometryFields(coordinates)
			_, err := decodeGeometry(document.payload(), testBoundaryID)
			assertProviderError(t, err)
		})
	}
}

func TestDecodeGeometryAcceptsExplicitZeroOrdinates(t *testing.T) {
	document := validGeometryDocument()
	document.Geometry = geometryFields(`[[[[0,0],[1,0],[1,1],[0,0]]]]`)
	value, err := decodeGeometry(document.payload(), testBoundaryID)
	if err != nil || len(value.Value) == 0 {
		t.Fatalf("decodeGeometry()=%+v error=%v", value, err)
	}
}

type geometryDocument struct {
	Collection string
	Feature    string
	Properties string
	Geometry   string
	CRS        string
}

func validGeometryDocument() geometryDocument {
	return geometryDocument{
		Collection: `"type":"FeatureCollection"`,
		Feature:    `"type":"Feature"`,
		Properties: propertyFields(testShapeID),
		Geometry: geometryFields(
			`[[[[116,39],[116.1,39],[116.1,39.1],[116,39]]]]`),
		CRS: testCRSObject,
	}
}

func (d geometryDocument) payload() []byte {
	crs := ""
	if d.CRS != "" {
		crs = `,"crs":` + d.CRS
	}
	return []byte(`{` + d.Collection + crs + `,"features":[{` + d.Feature +
		`,"properties":{` + d.Properties + `},"geometry":{` + d.Geometry + `}}]}`)
}

func propertyFields(shapeID string) string {
	return `"shapeID":"` + shapeID +
		`","shapeName":"China","shapeISO":"CHN","shapeGroup":"CHN","shapeType":"ADM0"`
}

func geometryFields(coordinates string) string {
	return `"type":"MultiPolygon","coordinates":` + coordinates
}

func validMetadataPayload() []byte {
	return []byte(`{"boundaryID":"` + testBoundaryID +
		`","boundaryName":"China","boundaryISO":"CHN","boundaryYearRepresented":"2019",` +
		`"boundaryType":"ADM0","boundarySource":"` + expectedSource +
		`","boundaryLicense":"` + expectedLicense +
		`","simplifiedGeometryGeoJSON":"` + testSourceURL + `"}`)
}

func decodeMetadataError(payload []byte) error {
	_, err := decodeMetadata(payload)
	return err
}

func assertProviderError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("error=%v want=%v", err, domain.ErrProviderUnavailable)
	}
}

func assertBoundaryBindingReference(t *testing.T, reference, metadataDigest, geometryDigest string) {
	t.Helper()
	parsed, err := url.Parse(reference)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	parsed.RawQuery = ""
	if parsed.Fragment != "" || parsed.String() != testMediaURL ||
		query.Get("boundaryID") != testBoundaryID ||
		query.Get("boundaryYear") != "2019" || query.Get("source") != expectedSource ||
		query.Get("license") != expectedLicense || query.Get("shapeID") != testShapeID ||
		query.Get("metadataSha256") != metadataDigest ||
		query.Get("geometrySha256") != geometryDigest {
		t.Fatalf("边界绑定证据无效: %s", reference)
	}
}

func boundaryProvider(t *testing.T, geometryBody, redirect string) (*Provider, *atomic.Int32) {
	return boundaryProviderWithMetadata(t, validMetadataPayload(), geometryBody, redirect)
}

func boundaryProviderWithMetadata(t *testing.T, metadataBody []byte,
	geometryBody, redirect string,
) (*Provider, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		switch request.URL.String() {
		case defaultMetadataURL:
			return boundaryResponse(request, http.StatusOK, string(metadataBody), ""), nil
		case testMediaURL:
			if redirect != "" {
				return boundaryResponse(request, http.StatusFound, "redirect", redirect), nil
			}
			return boundaryResponse(request, http.StatusOK, geometryBody, ""), nil
		default:
			return nil, fmt.Errorf("意外的 geoBoundaries 请求: %s", request.URL)
		}
	})
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Transport: transport},
		MaxAttempts: 1, Now: func() time.Time { return now }})
	provider, err := New(Options{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	return provider, requests
}

func boundaryResponse(request *http.Request, status int, body, location string) *http.Response {
	header := make(http.Header)
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{StatusCode: status, Header: header,
		Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
