package helpers

import "reflect"

func GetType(myvar any) string {
	return reflect.TypeOf(myvar).String()
}
