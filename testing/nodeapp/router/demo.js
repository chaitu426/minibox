import express from "express" 

const DemoRouter = express.Router();

DemoRouter.get("/", (req, res) => {
    res.send("Hello World!");
});

export default DemoRouter;
