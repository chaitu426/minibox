import express from "express"
import DemoRouter from "./router/demo.js"

const app = express();
const PORT = 3000;

app.get("/", (req, res) => {
    res.send("Hello World!");
});

app.use("/demo", DemoRouter);

app.listen(PORT, () => {
    console.log(`Server running on port ${PORT}`);
});
