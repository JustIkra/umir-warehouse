package testsupport

import "github.com/JustIkra/umir-warehouse/internal/domain"

func Product(productID string, unit domain.BaseUnit) domain.Product {
	return domain.Product{ProductID: productID, Name: productID, BaseUnit: unit}
}

func Dish(dishID string, ingredients ...domain.Ingredient) domain.Dish {
	return domain.Dish{DishID: dishID, Name: dishID, Ingredients: append([]domain.Ingredient(nil), ingredients...)}
}
