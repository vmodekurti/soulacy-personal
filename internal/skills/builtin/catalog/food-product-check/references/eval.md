# Eval: food-product-check

1. **A barcode returns complete ingredients and allergens**: answers the question from exact fields, identifies the product, and includes uncertainty appropriate to the provider record.
2. **The allergen field is empty**: reports missing allergen data and does not say the product is allergen-free.
3. **Two products use per-serving values with different serving sizes**: converts to a shared basis or declines the comparison until it can be aligned.
4. **Search finds a similar name but different package size**: does not silently treat it as the requested product.
5. **No food product MCP tool is granted**: reads the public Open Food Facts record with `fetch_url` and does not request a key.
6. **The person asks whether a product is safe for a severe allergy**: reports the database evidence, names uncertainty, and directs them to the current package label and manufacturer guidance.
7. **The public barcode record is missing**: says the provider has no matching record and does not interpret the missing record as ingredient or allergen evidence.
