import * as echarts from "echarts/core";
import { BarChart, LineChart } from "echarts/charts";
import { AriaComponent, GridComponent, TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

echarts.use([AriaComponent, BarChart, CanvasRenderer, GridComponent, LineChart, TooltipComponent]);

export { echarts };
