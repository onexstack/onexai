// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package printers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ErrUnexpectedObject is returned when a convertor is handed an object of a
// kind it does not know how to print.
var ErrUnexpectedObject = errors.New("unexpected object type")

// object is the intersection every printable API object satisfies: something a
// table row can be made of.
type object interface {
	runtime.Object
	metav1.Object
}

// column pairs one table column with the function that fills its cell. Keeping
// the two together is what stops the column order and the cell order from
// drifting apart: a row cannot be built without walking the columns.
type column[T object] struct {
	def  metav1.TableColumnDefinition
	cell func(T, time.Time) any
}

// convertor renders one resource as a metav1.Table.
//
// It is stateless and safe for concurrent use: the same instance serves every
// list, get and watch request for its resource.
type convertor[T object] struct {
	defs  []metav1.TableColumnDefinition
	cells []func(T, time.Time) any
}

// newConvertor returns a convertor for T with the given columns, in order.
func newConvertor[T object](columns ...column[T]) *convertor[T] {
	c := &convertor[T]{
		defs:  make([]metav1.TableColumnDefinition, 0, len(columns)),
		cells: make([]func(T, time.Time) any, 0, len(columns)),
	}
	for _, col := range columns {
		c.defs = append(c.defs, col.def)
		c.cells = append(c.cells, col.cell)
	}

	return c
}

// ConvertToTable implements rest.TableConvertor.
func (c *convertor[T]) ConvertToTable(_ context.Context, obj, tableOptions runtime.Object) (*metav1.Table, error) {
	if obj == nil {
		return nil, fmt.Errorf("%w: nil object", ErrUnexpectedObject)
	}

	rows, err := c.rows(obj)
	if err != nil {
		return nil, err
	}

	table := &metav1.Table{Rows: rows}
	inheritListMeta(table, obj)

	// NoHeaders is set by the watch encoder, which reuses one TableOptions
	// across events; read it, never write it.
	if options, ok := tableOptions.(*metav1.TableOptions); !ok || !options.NoHeaders {
		table.ColumnDefinitions = c.defs
	}

	return table, nil
}

// rows converts an object, or every item of a list, into table rows.
func (c *convertor[T]) rows(obj runtime.Object) ([]metav1.TableRow, error) {
	// One clock reading per request, so AGE does not wobble between rows.
	now := time.Now()

	if !meta.IsListType(obj) {
		row, err := c.row(obj, now)
		if err != nil {
			return nil, err
		}

		return []metav1.TableRow{row}, nil
	}

	rows := make([]metav1.TableRow, 0, meta.LenList(obj))
	err := meta.EachListItem(obj, func(item runtime.Object) error {
		row, err := c.row(item, now)
		if err != nil {
			return err
		}

		rows = append(rows, row)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk the items of %T: %w", obj, err)
	}

	return rows, nil
}

// row renders one object. The row carries the object itself: the response layer
// rewrites that field into PartialObjectMetadata (the default), the full object
// (--sort-by) or nothing, and a nil object there fails the request.
func (c *convertor[T]) row(item runtime.Object, now time.Time) (metav1.TableRow, error) {
	typed, ok := item.(T)
	if !ok {
		return metav1.TableRow{}, fmt.Errorf("%w: got %T", ErrUnexpectedObject, item)
	}

	cells := make([]any, 0, len(c.cells))
	for _, cell := range c.cells {
		cells = append(cells, cell(typed, now))
	}

	return metav1.TableRow{
		Cells:  cells,
		Object: runtime.RawExtension{Object: typed},
	}, nil
}

// inheritListMeta copies what the client needs to page through a list.
func inheritListMeta(table *metav1.Table, obj runtime.Object) {
	if accessor, err := meta.ListAccessor(obj); err == nil {
		table.ResourceVersion = accessor.GetResourceVersion()
		table.Continue = accessor.GetContinue()
		table.RemainingItemCount = accessor.GetRemainingItemCount()

		return
	}

	if accessor, err := meta.CommonAccessor(obj); err == nil {
		table.ResourceVersion = accessor.GetResourceVersion()
	}
}
