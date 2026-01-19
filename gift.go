/*
Package gift provides a set of useful image processing filters.

Basic usage:

	// 1. Create a new filter list and add some filters.
	g := gift.New(
	    gift.Resize(800, 0, gift.LanczosResampling),
	    gift.UnsharpMask(1, 1, 0),
	)

	// 2. Create a new image of the corresponding size.
	// dst is a new target image, src is the original image.
	dst := image.NewRGBA(g.Bounds(src.Bounds()))

	// 3. Use the Draw func to apply the filters to src and store the result in dst.
	g.Draw(dst, src)
*/
package gift

import (
	"image"
	"image/draw"
	"runtime"
)

// Filter is an image processing filter.
type Filter interface {
	// Draw applies the filter to the src image and outputs the result to the dst image.
	Draw(dst draw.Image, src image.Image, options *Options) error
	// Bounds calculates the appropriate bounds of an image after applying the filter.
	Bounds(srcBounds image.Rectangle) (dstBounds image.Rectangle)
}

// Options is the parameters passed to image processing filters.
type Options struct {
	Workers int
}

func (o *Options) init() {
	if o.Workers < 1 {
		// Values from benchmarking on a MacBook M1 with 10 logical CPUs.
		o.Workers = min(6, runtime.NumCPU())
	}
}

var defaultOptions = Options{}

func init() {
	defaultOptions.init()
}

// GIFT is a list of image processing filters.
type GIFT struct {
	filters []Filter
	options Options
}

// New creates a new filter list and initializes it with the given slice of filters.
func New(filters ...Filter) *GIFT {
	return &GIFT{
		filters: filters,
		options: defaultOptions,
	}
}

// NewWithOptions creates a new filter list with the given options and initializes it with the given slice of filters.
func NewWithOptions(options Options, filters ...Filter) *GIFT {
	options.init()
	return &GIFT{
		filters: filters,
		options: options,
	}
}

// Bounds calculates the appropriate bounds for the result image after applying all the added filters.
// Parameter srcBounds is the bounds of the source image.
//
// Example:
//
//	src := image.NewRGBA(image.Rect(0, 0, 100, 200))
//	g := gift.New(gift.Rotate90())
//
//	// calculate image bounds after applying rotation and create a new image of that size.
//	dst := image.NewRGBA(g.Bounds(src.Bounds())) // dst bounds: (0, 0, 200, 100)
func (g *GIFT) Bounds(srcBounds image.Rectangle) (dstBounds image.Rectangle) {
	b := srcBounds
	for _, f := range g.filters {
		b = f.Bounds(b)
	}
	dstBounds = b
	return
}

// Draw applies all the added filters to the src image and outputs the result to the dst image.
func (g *GIFT) Draw(dst draw.Image, src image.Image) error {
	if len(g.filters) == 0 {
		copyimage(dst, src, &g.options)
		return nil
	}

	first, last := 0, len(g.filters)-1
	var tmpIn image.Image
	var tmpOut draw.Image

	for i, f := range g.filters {
		if i == first {
			tmpIn = src
		} else {
			tmpIn = tmpOut
		}

		if i == last {
			tmpOut = dst
		} else {
			tmpOut = createTempImage(f.Bounds(tmpIn.Bounds()))
		}

		if err := f.Draw(tmpOut, tmpIn, &g.options); err != nil {
			return err
		}
	}
	return nil
}

// Operator is an image composition operator.
type Operator int

// Composition operators.
const (
	CopyOperator Operator = iota
	OverOperator
)

// DrawAt applies all the added filters to the src image and outputs the result to the dst image
// at the specified position pt using the specified composition operator op.
func (g *GIFT) DrawAt(dst draw.Image, src image.Image, pt image.Point, op Operator) error {
	switch op {
	case OverOperator:
		tb := g.Bounds(src.Bounds())
		tb = tb.Sub(tb.Min).Add(pt)
		tmp := createTempImage(tb)
		if err := g.Draw(tmp, src); err != nil {
			return err
		}
		pixGetterDst := newPixelGetter(dst)
		pixGetterTmp := newPixelGetter(tmp)
		pixSetterDst := newPixelSetter(dst)
		ib := tb.Intersect(dst.Bounds())
		parallelize(g.options.Workers, ib.Min.Y, ib.Max.Y, func(start, stop int) {
			for y := start; y < stop; y++ {
				for x := ib.Min.X; x < ib.Max.X; x++ {
					px0 := pixGetterDst.getPixel(x, y)
					px1 := pixGetterTmp.getPixel(x, y)
					c1 := px1.a
					c0 := (1 - c1) * px0.a
					cs := c0 + c1
					c0 /= cs
					c1 /= cs
					r := px0.r*c0 + px1.r*c1
					g := px0.g*c0 + px1.g*c1
					b := px0.b*c0 + px1.b*c1
					a := px0.a + px1.a*(1-px0.a)
					pixSetterDst.setPixel(x, y, pixel{r, g, b, a})
				}
			}
		})

	default:
		if pt.Eq(dst.Bounds().Min) {
			return g.Draw(dst, src)
		}
		if subimg, ok := getSubImage(dst, pt); ok {
			return g.Draw(subimg, src)
		}
		tb := g.Bounds(src.Bounds())
		tb = tb.Sub(tb.Min).Add(pt)
		tmp := createTempImage(tb)
		if err := g.Draw(tmp, src); err != nil {
			return err
		}
		pixGetter := newPixelGetter(tmp)
		pixSetter := newPixelSetter(dst)
		ib := tb.Intersect(dst.Bounds())
		parallelize(g.options.Workers, ib.Min.Y, ib.Max.Y, func(start, stop int) {
			for y := start; y < stop; y++ {
				for x := ib.Min.X; x < ib.Max.X; x++ {
					pixSetter.setPixel(x, y, pixGetter.getPixel(x, y))
				}
			}
		})
	}
	return nil
}

func getSubImage(img draw.Image, pt image.Point) (draw.Image, bool) {
	if !pt.In(img.Bounds()) {
		return nil, false
	}
	switch img := img.(type) {
	case *image.Gray:
		return img.SubImage(image.Rectangle{pt, img.Bounds().Max}).(draw.Image), true
	case *image.Gray16:
		return img.SubImage(image.Rectangle{pt, img.Bounds().Max}).(draw.Image), true
	case *image.RGBA:
		return img.SubImage(image.Rectangle{pt, img.Bounds().Max}).(draw.Image), true
	case *image.RGBA64:
		return img.SubImage(image.Rectangle{pt, img.Bounds().Max}).(draw.Image), true
	case *image.NRGBA:
		return img.SubImage(image.Rectangle{pt, img.Bounds().Max}).(draw.Image), true
	case *image.NRGBA64:
		return img.SubImage(image.Rectangle{pt, img.Bounds().Max}).(draw.Image), true
	default:
		return nil, false
	}
}
