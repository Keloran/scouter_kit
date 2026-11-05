package scouterkit.ssh

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Rect
import android.view.View

// The two emulated arms' panels, left and right, refreshed four times a second (the emulated flavour, R12).
class PanelView(context: Context) : View(context) {
    private val bitmaps = Array(2) { Bitmap.createBitmap(640, 480, Bitmap.Config.ARGB_8888) }
    private val pixels = IntArray(640 * 480)
    private val green = IntArray(16) { Color.rgb(0, it * 17, 0) }
    private val refresh = object : Runnable {
        override fun run() {
            for (arm in 0..1) {
                val p = Native.emulatedPanel(arm) ?: continue
                for (i in pixels.indices) pixels[i] = green[p[i].toInt() and 15]
                bitmaps[arm].setPixels(pixels, 0, 640, 0, 0, 640, 480)
            }
            invalidate()
            postDelayed(this, 250)
        }
    }

    override fun onAttachedToWindow() {
        super.onAttachedToWindow()
        post(refresh)
    }

    override fun onDetachedFromWindow() {
        removeCallbacks(refresh)
        super.onDetachedFromWindow()
    }

    override fun onMeasure(widthSpec: Int, heightSpec: Int) {
        val w = MeasureSpec.getSize(widthSpec)
        setMeasuredDimension(w, w * 480 / 1280)
    }

    override fun onDraw(canvas: Canvas) {
        val w = width / 2
        for (arm in 0..1) canvas.drawBitmap(bitmaps[arm], null, Rect(arm * w, 0, arm * w + w - 2, height), null)
    }
}
