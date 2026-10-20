ALTER TABLE orders ALTER COLUMN status TYPE TEXT;
DROP TYPE order_status;
CREATE TYPE order_status AS ENUM ('pending', 'batched', 'assigned', 'picked_up', 'delivered', 'cancelled');
ALTER TABLE orders ALTER COLUMN status TYPE order_status USING status::order_status;
