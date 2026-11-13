package scouterkit.chewedfeed

import android.app.Activity
import android.os.Bundle
import android.text.InputType
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import java.util.Locale

// App settings. For now this phone's display settings for the glasses; setup, calibration and ownership
// controls come with CF-45.
class SettingsActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setTitle(R.string.settings)
        val current = DisplaySettings.load(this)
        val list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(32, 32, 32, 32) }
        fun field(label: Int, value: Int) = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_FLAG_SIGNED
            setText(String.format(Locale.ROOT, "%d", value)) // ASCII digits: parsed back with toIntOrNull
        }.also {
            list.addView(TextView(this).apply { setText(label) })
            list.addView(it)
        }
        list.addView(TextView(this).apply { setText(R.string.display_settings) })
        val dx = field(R.string.dx_label, current.dx)
        val dy = field(R.string.dy_label, current.dy)
        val scale = field(R.string.scale_label, current.scale)
        val state = TextView(this)
        list.addView(Button(this).apply {
            setText(R.string.save)
            setOnClickListener {
                val s = DisplaySettings(dx.text.toString().toIntOrNull() ?: Int.MIN_VALUE,
                    dy.text.toString().toIntOrNull() ?: Int.MIN_VALUE, scale.text.toString().toIntOrNull() ?: 0)
                val problem = s.problem()
                if (problem != null) {
                    state.text = problem
                } else {
                    s.save(this@SettingsActivity)
                    state.setText(R.string.saved)
                }
            }
        })
        list.addView(state)
        setContentView(ScrollView(this).apply { addView(list) })
    }
}
